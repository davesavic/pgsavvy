package logs

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
)

// Redactor scrubs secret material from a slog.Record in-place. The default
// implementation walks the record's attrs reflectively, redacts fields tagged
// `log:"redact"`, applies DSN-credential regex scrubs to all string-typed
// values + the record's message, and replaces values that exactly match any
// env-var whose name contains PASSWORD/SECRET/TOKEN.
type Redactor interface {
	Redact(*slog.Record)
}

// DefaultRedactor returns the production redactor used by Open() to scrub
// secrets before log lines reach disk. Safe for concurrent calls.
func DefaultRedactor() Redactor { return &defaultRedactor{} }

// Redaction placeholder constants. Kept exported-ish for test assertions
// via package-internal access.
const (
	redactedMarker      = "[REDACTED]"
	redactedDepthMarker = "[REDACTED:depth]"
	maxWalkDepth        = 5
)

// Duplicated from pkg/session/profile.go to avoid an import cycle if/when
// pkg/session grows to depend on pkg/logs. Keep these in sync with the
// canonical definitions there.
var (
	logsDSNInlineCredRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/@?\s]+):[^@/?\s]+@`)
	logsKVDSNCredRe     = regexp.MustCompile(`(?i)\b(password|sslpassword)=('[^']*'|"[^"]*"|\S+)`)
)

type defaultRedactor struct {
	depthWarnOnce sync.Once
}

// Redact mutates *r in place: it rebuilds the record's attrs with redacted
// copies and scrubs r.Message. A panic during redaction leaves the record
// unchanged (the caller's redactingHandler ALSO defers recover for safety).
func (r *defaultRedactor) Redact(rec *slog.Record) {
	defer func() {
		_ = recover()
	}()

	envSecrets := buildEnvSecretSet()

	// Collect original attrs.
	origAttrs := make([]slog.Attr, 0, rec.NumAttrs())
	rec.Attrs(func(a slog.Attr) bool {
		origAttrs = append(origAttrs, a)
		return true
	})

	// Rebuild attrs with redacted values.
	newAttrs := make([]slog.Attr, 0, len(origAttrs))
	for _, a := range origAttrs {
		newAttrs = append(newAttrs, r.redactAttr(a, envSecrets, 0))
	}

	newMsg := scrubString(rec.Message, envSecrets)

	// Build a new record (slog.Record's attrs are append-only).
	nr := slog.NewRecord(rec.Time, rec.Level, newMsg, rec.PC)
	nr.AddAttrs(newAttrs...)
	*rec = nr
}

// redactAttr returns a new slog.Attr whose value has been recursively
// redacted. Strings get scrubString applied; structs (incl. via Any) get
// reflected-into; the kindGroup case recurses.
func (r *defaultRedactor) redactAttr(a slog.Attr, env map[string]struct{}, depth int) slog.Attr {
	val := a.Value.Resolve()
	switch val.Kind() {
	case slog.KindString:
		return slog.String(a.Key, scrubString(val.String(), env))
	case slog.KindGroup:
		nested := val.Group()
		out := make([]slog.Attr, 0, len(nested))
		for _, sub := range nested {
			out = append(out, r.redactAttr(sub, env, depth+1))
		}
		return slog.Group(a.Key, attrsToAny(out)...)
	case slog.KindAny:
		any := r.redactValue(reflect.ValueOf(val.Any()), env, depth)
		return slog.Any(a.Key, any)
	default:
		// Numbers, bools, time, duration — pass through.
		return a
	}
}

// attrsToAny converts []slog.Attr to []any for the slog.Group variadic.
func attrsToAny(in []slog.Attr) []any {
	out := make([]any, len(in))
	for i, a := range in {
		out[i] = a
	}
	return out
}

// buildEnvSecretSet rebuilds the env-value allowlist on every Redact so vars
// set AFTER the redactor was constructed are still scrubbed.
func buildEnvSecretSet() map[string]struct{} {
	out := make(map[string]struct{})
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		name := kv[:eq]
		val := kv[eq+1:]
		if val == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN") {
			out[val] = struct{}{}
		}
	}
	return out
}

// redactValue returns a redacted copy of v. It NEVER mutates v's underlying
// data. For structs/pointers/slices/maps it builds a parallel
// map[string]any / []any tree.
func (r *defaultRedactor) redactValue(v reflect.Value, env map[string]struct{}, depth int) any {
	if depth >= maxWalkDepth {
		r.depthWarnOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "logs: redactor depth cap hit")
		})
		return redactedDepthMarker
	}
	if !v.IsValid() {
		return nil
	}

	// Unwrap interface.
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	// Unwrap pointer.
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.String:
		return scrubString(v.String(), env)
	case reflect.Struct:
		return r.redactStruct(v, env, depth)
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = r.redactValue(v.Index(i), env, depth+1)
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			out[key] = r.redactValue(iter.Value(), env, depth+1)
		}
		return out
	default:
		// Numbers, bools, etc. — passthrough via Interface(). Guard against
		// unexported / unaddressable fields by checking CanInterface.
		if v.CanInterface() {
			return v.Interface()
		}
		return nil
	}
}

// redactStruct produces a map[string]any of fieldName -> (maybe redacted)
// value. Field name resolution prefers `json` tag, then `yaml`, then Go
// name.
func (r *defaultRedactor) redactStruct(v reflect.Value, env map[string]struct{}, depth int) any {
	t := v.Type()
	out := make(map[string]any, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name := fieldName(sf)
		if sf.Tag.Get("log") == "redact" {
			out[name] = redactedMarker
			continue
		}
		out[name] = r.redactValue(v.Field(i), env, depth+1)
	}
	return out
}

func fieldName(sf reflect.StructField) string {
	if tag := sf.Tag.Get("json"); tag != "" {
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}
		if tag != "" && tag != "-" {
			return tag
		}
	}
	if tag := sf.Tag.Get("yaml"); tag != "" {
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}
		if tag != "" && tag != "-" {
			return tag
		}
	}
	return sf.Name
}

// scrubString applies env-exact-match, URL-form DSN regex, and kv-form DSN
// regex in that order. Returns s unchanged when no rule matches.
func scrubString(s string, env map[string]struct{}) string {
	if s == "" {
		return s
	}
	if _, ok := env[s]; ok {
		return redactedMarker
	}
	s = logsDSNInlineCredRe.ReplaceAllString(s, "${1}${2}:***@")
	s = logsKVDSNCredRe.ReplaceAllString(s, "$1=***")
	return s
}
