package session

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readMachinePgpassFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	defer func() { _ = file.Close() }()
	return readMachinePgpassHandle(file)
}

func readMachinePgpassHandle(file *os.File) ([]byte, error) {
	unavailable := &MachineCredentialError{Kind: MachineCredentialUnavailable}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, unavailable
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return nil, unavailable
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, unavailable
	}
	trusted := [][]byte{unsafe.Slice((*byte)(unsafe.Pointer(user.User.Sid)), user.User.Sid.Len())}
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(kind)
		if err != nil || sid == nil || !sid.IsValid() {
			return nil, unavailable
		}
		trusted = append(trusted, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len()))
	}
	// Only OS-produced descriptors reach Length. Forged/malformed fixtures use
	// the bounded byte parser directly, never native calls on forged pointers.
	descriptor := unsafe.Slice((*byte)(unsafe.Pointer(sd)), int(sd.Length()))
	private := machinePgpassDescriptorPrivate(descriptor, trusted)
	runtime.KeepAlive(sd)
	if !private {
		return nil, unavailable
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, unavailable
	}
	return data, nil
}

func machinePgpassDescriptorPrivate(data []byte, trusted [][]byte) bool {
	const headerSize = 20
	if len(data) < headerSize || data[0] != 1 {
		return false
	}
	control := binary.LittleEndian.Uint16(data[2:4])
	required := uint16(windows.SE_SELF_RELATIVE | windows.SE_DACL_PRESENT)
	if control&required != required {
		return false
	}
	trustedSID := func(sid []byte) bool {
		for _, allowed := range trusted {
			if bytes.Equal(sid, allowed) {
				return true
			}
		}
		return false
	}
	component := func(field int) ([]byte, bool) {
		offset := binary.LittleEndian.Uint32(data[field : field+4])
		if offset < headerSize || offset%4 != 0 || uint64(offset) >= uint64(len(data)) {
			return nil, false
		}
		return data[int(offset):], true
	}
	owner, ok := component(4)
	if !ok {
		return false
	}
	owner, ok = machinePgpassSID(owner)
	if !ok || !trustedSID(owner) {
		return false
	}
	if binary.LittleEndian.Uint32(data[8:12]) != 0 {
		group, ok := component(8)
		if !ok {
			return false
		}
		if _, ok := machinePgpassSID(group); !ok {
			return false
		}
	}
	// SACLs are not requested by the handle query; unexpected forms fail closed.
	if binary.LittleEndian.Uint32(data[12:16]) != 0 {
		return false
	}
	acl, ok := component(16)
	if !ok || len(acl) < 8 || acl[0] != 2 && acl[0] != 4 || acl[1] != 0 || binary.LittleEndian.Uint16(acl[6:8]) != 0 {
		return false
	}
	size := int(binary.LittleEndian.Uint16(acl[2:4]))
	if size < 8 || size%4 != 0 || size > len(acl) {
		return false
	}
	acl = acl[:size]
	count := int(binary.LittleEndian.Uint16(acl[4:6]))
	position := 8
	for range count {
		if len(acl)-position < 4 {
			return false
		}
		ace := acl[position:]
		aceSize := int(binary.LittleEndian.Uint16(ace[2:4]))
		if aceSize < 16 || aceSize%4 != 0 || aceSize > len(ace) || ace[1]&^byte(windows.VALID_INHERIT_FLAGS) != 0 {
			return false
		}
		if ace[0] != windows.ACCESS_ALLOWED_ACE_TYPE && ace[0] != windows.ACCESS_DENIED_ACE_TYPE {
			return false
		}
		sid, ok := machinePgpassSID(ace[8:aceSize])
		if !ok || len(sid)+8 != aceSize {
			return false
		}
		if ace[0] == windows.ACCESS_ALLOWED_ACE_TYPE && ace[1]&windows.INHERIT_ONLY_ACE == 0 && binary.LittleEndian.Uint32(ace[4:8]) != 0 && !trustedSID(sid) {
			return false
		}
		position += aceSize
	}
	return true
}

func machinePgpassSID(data []byte) ([]byte, bool) {
	if len(data) < 8 || data[0] != 1 || data[1] > 15 {
		return nil, false
	}
	size := 8 + 4*int(data[1])
	if len(data) < size {
		return nil, false
	}
	return data[:size], true
}
