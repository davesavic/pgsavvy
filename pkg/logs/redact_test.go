package logs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// fireEntry constructs a logrus.Entry with the given data + message, runs
// the given redactor's Fire(), and returns the (possibly mutated) entry.
// Using a fresh Logger per call keeps tests isolated from package-level
// state.
func fireEntry(t *testing.T, r Redactor, msg string, data logrus.Fields) *logrus.Entry {
	t.Helper()
	l := logrus.New()
	l.Out = &bytes.Buffer{}
	entry := logrus.NewEntry(l)
	entry.Data = logrus.Fields{}
	for k, v := range data {
		entry.Data[k] = v
	}
	entry.Message = msg
	entry.Time = time.Now()
	if err := r.Fire(entry); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	return entry
}

// flatten serializes the entry's Data via JSON and returns the resulting
// string. Reflective walking produces map[string]any trees, which JSON
// can render deterministically enough for assertion-by-substring.
func flatten(t *testing.T, e *logrus.Entry) string {
	t.Helper()
	b, err := json.Marshal(e.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestDefaultRedactor_StructTagFields(t *testing.T) {
	r := DefaultRedactor()
	conn := models.Connection{
		Name:            "primary",
		Driver:          "postgres",
		DSN:             "postgres://u:s3cret@host/db",
		Password:        "hunter2",
		PasswordCommand: "pass mydb",
		KeyringRef:      "kr-id",
		PgpassPath:      "/home/me/.pgpass",
		SSHTunnel: &models.SSHTunnelConfig{
			Host:         "bastion",
			User:         "tunnel",
			Port:         22,
			IdentityFile: "/home/me/.ssh/id_rsa",
		},
	}
	e := fireEntry(t, r, "open", logrus.Fields{"conn": conn})
	out := flatten(t, e)

	// All tagged fields appear as [REDACTED].
	for _, tagged := range []string{"dsn", "password", "password_command", "keyring", "pgpass"} {
		// match e.g. "dsn":"[REDACTED]"
		needle := fmt.Sprintf(`"%s":"[REDACTED]"`, tagged)
		if !strings.Contains(out, needle) {
			t.Errorf("expected %q in output, got %s", needle, out)
		}
	}
	// SSHTunnel.IdentityFile is nested.
	if !strings.Contains(out, `"identity_file":"[REDACTED]"`) {
		t.Errorf("expected identity_file to be redacted, got %s", out)
	}
	// Non-tagged fields survive.
	if !strings.Contains(out, `"name":"primary"`) {
		t.Errorf("expected name=primary to survive, got %s", out)
	}
	// Original struct value is NOT mutated.
	if conn.Password != "hunter2" {
		t.Errorf("redactor mutated source: Password=%q", conn.Password)
	}
}

func TestDefaultRedactor_DSNRegex_URLForm(t *testing.T) {
	r := DefaultRedactor()
	e := fireEntry(t, r, "opening postgres://u:s3cret@h/d", logrus.Fields{
		"dsn": "postgres://u:s3cret@h/d",
	})
	if !strings.Contains(e.Message, "postgres://u:***@h/d") {
		t.Errorf("message not redacted: %q", e.Message)
	}
	if got := e.Data["dsn"].(string); got != "postgres://u:***@h/d" {
		t.Errorf("data[dsn] = %q, want postgres://u:***@h/d", got)
	}
}

func TestDefaultRedactor_DSNRegex_KVForm(t *testing.T) {
	r := DefaultRedactor()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "host=h password=hunter2 user=u", "host=h password=*** user=u"},
		{"sslpassword", "host=h sslpassword=hunter2 user=u", "host=h sslpassword=*** user=u"},
		{"single-quoted", "host=h password='hunter 2' user=u", "host=h password=*** user=u"},
		{"upper", "host=h PASSWORD=hunter2", "host=h PASSWORD=***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fireEntry(t, r, tc.in, logrus.Fields{"raw": tc.in})
			if e.Message != tc.want {
				t.Errorf("message: got %q want %q", e.Message, tc.want)
			}
			if got := e.Data["raw"].(string); got != tc.want {
				t.Errorf("data[raw]: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultRedactor_EnvAddedAfterStart_StillRedacted(t *testing.T) {
	r := DefaultRedactor() // constructed BEFORE env var is set
	t.Setenv("MY_TEST_PASSWORD", "topsecret_unique_value_xyz")
	e := fireEntry(t, r, "noop", logrus.Fields{"raw": "topsecret_unique_value_xyz"})
	if got := e.Data["raw"].(string); got != redactedMarker {
		t.Errorf("data[raw] = %q, want %q", got, redactedMarker)
	}
}

func TestDefaultRedactor_InterfaceUnwrap(t *testing.T) {
	r := DefaultRedactor()
	var iface interface{} = &models.Connection{Password: "x", Name: "n"}
	e := fireEntry(t, r, "noop", logrus.Fields{"any": iface})
	out := flatten(t, e)
	if !strings.Contains(out, `"password":"[REDACTED]"`) {
		t.Errorf("interface-wrapped pointer not redacted: %s", out)
	}
}

func TestDefaultRedactor_SliceOfStruct(t *testing.T) {
	r := DefaultRedactor()
	conns := []models.Connection{
		{Name: "a", Password: "p1"},
		{Name: "b", Password: "p2"},
	}
	e := fireEntry(t, r, "noop", logrus.Fields{"conns": conns})
	out := flatten(t, e)
	// Both passwords redacted; names visible.
	if strings.Contains(out, "p1") || strings.Contains(out, "p2") {
		t.Errorf("password leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"a"`) || !strings.Contains(out, `"name":"b"`) {
		t.Errorf("names missing: %s", out)
	}
}

func TestDefaultRedactor_MapOfStruct(t *testing.T) {
	r := DefaultRedactor()
	m := map[string]models.Connection{
		"a": {Name: "a", Password: "p1"},
		"b": {Name: "b", Password: "p2"},
	}
	e := fireEntry(t, r, "noop", logrus.Fields{"conns": m})
	out := flatten(t, e)
	if strings.Contains(out, "p1") || strings.Contains(out, "p2") {
		t.Errorf("password leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"a"`) || !strings.Contains(out, `"name":"b"`) {
		t.Errorf("names missing: %s", out)
	}
}

// cyclic is intentionally self-referential to exercise the depth cap.
type cyclic struct {
	Name string
	Self *cyclic
}

func TestDefaultRedactor_DepthCap(t *testing.T) {
	r := DefaultRedactor()
	root := &cyclic{Name: "root"}
	root.Self = root // cycle

	done := make(chan struct{})
	go func() {
		_ = fireEntry(t, r, "noop", logrus.Fields{"c": root})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("redactor did not return — depth cap not enforced")
	}
}

func TestDefaultRedactor_Idempotent(t *testing.T) {
	r := DefaultRedactor()
	e := fireEntry(t, r, "opening postgres://u:s3cret@h/d", logrus.Fields{
		"conn": models.Connection{Password: "x", Name: "n"},
		"dsn":  "postgres://u:s3cret@h/d",
	})
	firstMsg := e.Message
	firstData := flatten(t, e)

	// Second pass through the same redactor on the already-redacted entry.
	if err := r.Fire(e); err != nil {
		t.Fatalf("Fire 2: %v", err)
	}
	if e.Message != firstMsg {
		t.Errorf("message changed on second pass: %q -> %q", firstMsg, e.Message)
	}
	if flatten(t, e) != firstData {
		t.Errorf("data changed on second pass: %s -> %s", firstData, flatten(t, e))
	}
}

func TestDefaultRedactor_RaceFree(t *testing.T) {
	r := DefaultRedactor()
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				c := models.Connection{
					Name:     fmt.Sprintf("g%d-%d", g, i),
					Password: "hunter2",
					DSN:      "postgres://u:p@h/d",
				}
				_ = fireEntry(t, r, "opening postgres://u:p@h/d", logrus.Fields{"conn": c})
			}
		}(g)
	}
	wg.Wait()
}
