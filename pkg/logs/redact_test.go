package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// redactRecord builds a slog.Record with the given message + attrs, runs it
// through DefaultRedactor (or the given Redactor), and returns the mutated
// record.
func redactRecord(t *testing.T, r Redactor, msg string, attrs ...slog.Attr) slog.Record {
	t.Helper()
	rec := slog.NewRecord(time.Now(), slog.LevelDebug, msg, 0)
	rec.AddAttrs(attrs...)
	r.Redact(&rec)
	return rec
}

// flattenAttrs returns a JSON-marshaled map of the record's top-level attrs
// (resolving Any values to their interface form via reflection).
func flattenAttrs(t *testing.T, r slog.Record) string {
	t.Helper()
	out := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindAny {
			out[a.Key] = v.Any()
		} else {
			out[a.Key] = v.String()
		}
		return true
	})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestRedactingHandler_StructTag_ScrubsLogRedact(t *testing.T) {
	conn := models.Connection{
		Name:            "primary",
		Driver:          "postgres",
		DSN:             "postgres://u:s3cret@host/db",
		Password:        "hunter2",
		PasswordCommand: "pass mydb",
		KeyringRef:      "kr-id",
		PgpassPath:      "/home/me/.pgpass",
		SSHTunnel: &models.SSHTunnelConfig{
			Host:              "bastion",
			User:              "tunnel",
			Port:              22,
			IdentityFile:      "/home/me/.ssh/id_rsa",
			PassphraseCommand: "pass show ssh/key",
		},
	}

	rh := NewRecordingHandler()
	h := &redactingHandler{next: rh, redactor: DefaultRedactor()}
	rec := slog.NewRecord(time.Now(), slog.LevelDebug, "open", 0)
	rec.AddAttrs(slog.Any("conn", conn))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	recs := rh.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	out := flattenAttrs(t, recs[0])

	for _, tagged := range []string{"dsn", "password", "password_command", "keyring", "pgpass"} {
		needle := fmt.Sprintf(`"%s":"[REDACTED]"`, tagged)
		if !strings.Contains(out, needle) {
			t.Errorf("expected %q in output, got %s", needle, out)
		}
	}
	if !strings.Contains(out, `"identity_file":"[REDACTED]"`) {
		t.Errorf("expected identity_file to be redacted, got %s", out)
	}
	if !strings.Contains(out, `"passphrase_command":"[REDACTED]"`) {
		t.Errorf("expected passphrase_command to be redacted, got %s", out)
	}
	if strings.Contains(out, "pass show ssh/key") {
		t.Errorf("passphrase command leaked in plaintext: %s", out)
	}
	if !strings.Contains(out, `"name":"primary"`) {
		t.Errorf("expected name=primary to survive, got %s", out)
	}
	// Original struct must not be mutated.
	if conn.Password != "hunter2" {
		t.Errorf("redactor mutated source: Password=%q", conn.Password)
	}
}

func TestRedactingHandler_DepthCap_5(t *testing.T) {
	type cyclic struct {
		Name string
		Self *cyclic
	}
	root := &cyclic{Name: "root"}
	root.Self = root

	rh := NewRecordingHandler()
	h := &redactingHandler{next: rh, redactor: DefaultRedactor()}

	done := make(chan struct{})
	go func() {
		rec := slog.NewRecord(time.Now(), slog.LevelDebug, "noop", 0)
		rec.AddAttrs(slog.Any("c", root))
		_ = h.Handle(context.Background(), rec)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("redactor did not return — depth cap not enforced")
	}
}

// panickyRedactor panics on Redact; the wrapping redactingHandler must
// recover and forward the original record unchanged.
type panickyRedactor struct{}

func (panickyRedactor) Redact(_ *slog.Record) { panic("boom") }

func TestRedactingHandler_PanicSafe_Recovers(t *testing.T) {
	rh := NewRecordingHandler()
	h := &redactingHandler{next: rh, redactor: panickyRedactor{}}

	rec := slog.NewRecord(time.Now(), slog.LevelDebug, "untouched", 0)
	rec.AddAttrs(slog.String("k", "v"))

	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	recs := rh.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].Message != "untouched" {
		t.Errorf("message changed: %q", recs[0].Message)
	}
	if v, ok := attrByKey(recs[0], "k"); !ok || v.String() != "v" {
		t.Errorf("attr k = %v ok=%v, want v", v, ok)
	}
}

func TestDefaultRedactor_DSNRegex_URLForm(t *testing.T) {
	r := DefaultRedactor()
	rec := redactRecord(t, r, "opening postgres://u:s3cret@h/d", slog.String("dsn", "postgres://u:s3cret@h/d"))
	if !strings.Contains(rec.Message, "postgres://u:***@h/d") {
		t.Errorf("message not redacted: %q", rec.Message)
	}
	v, _ := attrByKey(rec, "dsn")
	if v.String() != "postgres://u:***@h/d" {
		t.Errorf("dsn = %q, want postgres://u:***@h/d", v.String())
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
			rec := redactRecord(t, r, tc.in, slog.String("raw", tc.in))
			if rec.Message != tc.want {
				t.Errorf("message: got %q want %q", rec.Message, tc.want)
			}
			v, _ := attrByKey(rec, "raw")
			if v.String() != tc.want {
				t.Errorf("raw: got %q want %q", v.String(), tc.want)
			}
		})
	}
}

func TestDefaultRedactor_EnvAddedAfterStart_StillRedacted(t *testing.T) {
	r := DefaultRedactor() // constructed before env is set
	t.Setenv("MY_TEST_PASSWORD", "topsecret_unique_value_xyz")
	rec := redactRecord(t, r, "noop", slog.String("raw", "topsecret_unique_value_xyz"))
	v, _ := attrByKey(rec, "raw")
	if v.String() != redactedMarker {
		t.Errorf("raw = %q, want %q", v.String(), redactedMarker)
	}
}

func TestDefaultRedactor_InterfaceUnwrap(t *testing.T) {
	r := DefaultRedactor()
	var iface interface{} = &models.Connection{Password: "x", Name: "n"}
	rec := redactRecord(t, r, "noop", slog.Any("any", iface))
	out := flattenAttrs(t, rec)
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
	rec := redactRecord(t, r, "noop", slog.Any("conns", conns))
	out := flattenAttrs(t, rec)
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
	rec := redactRecord(t, r, "noop", slog.Any("conns", m))
	out := flattenAttrs(t, rec)
	if strings.Contains(out, "p1") || strings.Contains(out, "p2") {
		t.Errorf("password leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"a"`) || !strings.Contains(out, `"name":"b"`) {
		t.Errorf("names missing: %s", out)
	}
}

func TestDefaultRedactor_Idempotent(t *testing.T) {
	r := DefaultRedactor()
	rec1 := redactRecord(t, r, "opening postgres://u:s3cret@h/d",
		slog.Any("conn", models.Connection{Password: "x", Name: "n"}),
		slog.String("dsn", "postgres://u:s3cret@h/d"),
	)
	firstMsg := rec1.Message
	firstFlat := flattenAttrs(t, rec1)

	// Second pass: feed the already-redacted record back in.
	rec2 := rec1.Clone()
	r.Redact(&rec2)
	if rec2.Message != firstMsg {
		t.Errorf("message changed: %q -> %q", firstMsg, rec2.Message)
	}
	if flattenAttrs(t, rec2) != firstFlat {
		t.Errorf("attrs changed:\nfirst:  %s\nsecond: %s", firstFlat, flattenAttrs(t, rec2))
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
				_ = redactRecord(t, r, "opening postgres://u:p@h/d", slog.Any("conn", c))
			}
		}(g)
	}
	wg.Wait()
}
