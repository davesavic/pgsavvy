package session

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func machinePgpassTestUser(t *testing.T) *windows.SID {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid
}

func machinePgpassTestDescriptor(t *testing.T, sddl string) []byte {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(sd)), int(sd.Length())))
	runtime.KeepAlive(sd)
	return data
}

func machinePgpassTestFile(t *testing.T, path, sddl, contents string) *os.File {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("fixture file wrapping failed")
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.WriteString(contents); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	return file
}

func machinePgpassTestSecurity(t *testing.T, file *os.File) string {
	t.Helper()
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || sd.String() == "" {
		t.Fatal("fixture descriptor query failed")
	}
	return sd.String()
}

func TestMachinePgpass_WindowsACL(t *testing.T) {
	user := machinePgpassTestUser(t).String()
	private := "O:" + user + "D:P(A;;FA;;;" + user + ")(A;;FA;;;SY)(A;;FA;;;BA)"
	for _, rights := range []string{"", "GR", "GW", "GA", "RC", "SD", "FX", "WD", "WO"} {
		t.Run("foreign-"+rights, func(t *testing.T) {
			sddl := private
			if rights != "" {
				sddl += "(A;;" + rights + ";;;WD)"
			}
			path := filepath.Join(t.TempDir(), "pgpass λ with spaces")
			file := machinePgpassTestFile(t, path, sddl, "synthetic-password")
			before := machinePgpassTestSecurity(t, file)
			data, err := readMachinePgpassFile(path)
			if rights == "" {
				if err != nil || string(data) != "synthetic-password" {
					t.Fatal("private descriptor rejected")
				}
			} else {
				var safe *MachineCredentialError
				if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable || data != nil {
					t.Fatal("foreign grant accepted or returned bytes")
				}
			}
			if machinePgpassTestSecurity(t, file) != before {
				t.Fatal("pgpass reader repaired permissions")
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil || string(contents) != "synthetic-password" {
				t.Fatal("pgpass reader modified contents")
			}
		})
	}

	parent := filepath.Join(t.TempDir(), "inherited")
	sd, err := windows.SecurityDescriptorFromString("O:" + user + "D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	wide, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		t.Fatal(err)
	}
	err = windows.CreateDirectory(wide, &sa)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "inherited.pgpass")
	if err := os.WriteFile(path, []byte("inherited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := readMachinePgpassFile(path); err != nil || string(data) != "inherited" {
		t.Fatal("restricted inherited descriptor rejected")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	inherited, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := inherited.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		t.Fatal("fixture did not inherit an ACL")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil || ace.Header.AceFlags&windows.INHERITED_ACE == 0 {
		t.Fatal("fixture does not prove actual inheritance")
	}
}

func TestMachinePgpass_WindowsDescriptor(t *testing.T) {
	user := machinePgpassTestUser(t)
	trusted := [][]byte{bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(user)), user.Len()))}
	private := "O:" + user.String() + "D:P(A;;FA;;;" + user.String() + ")"
	for _, tc := range []struct {
		sddl string
		want bool
	}{
		{private, true},
		{"O:WDD:P(A;;FA;;;" + user.String() + ")", false},
		{"O:" + user.String() + "D:NO_ACCESS_CONTROL", false},
		{"O:" + user.String() + "D:P", true},
		{private + "(A;IO;GR;;;WD)", true},
		{"O:" + user.String() + "D:P(D;;GR;;;WD)(A;;GR;;;WD)", false},
	} {
		data := machinePgpassTestDescriptor(t, tc.sddl)
		if got := machinePgpassDescriptorPrivate(data, trusted); got != tc.want {
			t.Fatal("descriptor owner/null/empty/inherit-only/deny policy changed")
		}
	}
	valid := machinePgpassTestDescriptor(t, private)
	for size := range len(valid) {
		if machinePgpassDescriptorPrivate(valid[:size], trusted) {
			t.Fatal("truncated descriptor accepted")
		}
	}
	for _, mutate := range []func([]byte){
		func(b []byte) { b[0] = 2 },
		func(b []byte) { b[3] &^= 0x80 },
		func(b []byte) { binary.LittleEndian.PutUint32(b[4:8], ^uint32(0)) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[16:20], 0) },
		func(b []byte) { acl := b[binary.LittleEndian.Uint32(b[16:20]):]; acl[0] = 3 },
		func(b []byte) {
			acl := b[binary.LittleEndian.Uint32(b[16:20]):]
			binary.LittleEndian.PutUint16(acl[2:4], 4)
		},
		func(b []byte) {
			acl := b[binary.LittleEndian.Uint32(b[16:20]):]
			binary.LittleEndian.PutUint16(acl[4:6], 65535)
		},
		func(b []byte) { acl := b[binary.LittleEndian.Uint32(b[16:20]):]; acl[8] = 0x11 },
		func(b []byte) { acl := b[binary.LittleEndian.Uint32(b[16:20]):]; acl[9] = 0x80 },
		func(b []byte) {
			acl := b[binary.LittleEndian.Uint32(b[16:20]):]
			binary.LittleEndian.PutUint16(acl[10:12], 12)
		},
		func(b []byte) { acl := b[binary.LittleEndian.Uint32(b[16:20]):]; acl[17] = 255 },
	} {
		data := bytes.Clone(valid)
		mutate(data)
		if machinePgpassDescriptorPrivate(data, trusted) {
			t.Fatal("malformed/unknown descriptor accepted")
		}
	}
}

func TestMachinePgpass_CheckedHandle(t *testing.T) {
	user := machinePgpassTestUser(t).String()
	private := "O:" + user + "D:P(A;;FA;;;" + user + ")"
	path := filepath.Join(t.TempDir(), "pgpass")
	file := machinePgpassTestFile(t, path, private, "checked")
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	machinePgpassTestFile(t, path, private+"(A;;GR;;;WD)", "unchecked")
	if data, err := readMachinePgpassHandle(file); err != nil || string(data) != "checked" {
		t.Fatal("path replacement switched checked-handle contents")
	}
	if data, err := readMachinePgpassFile(path); err == nil || data != nil {
		t.Fatal("replacement privacy was not checked")
	}
}
