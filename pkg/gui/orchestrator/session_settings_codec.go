package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func decodedSetting(values ...string) common.CanonicalSessionSetting {
	return common.CanonicalSessionSetting{Values: values, Provenance: "decoded"}
}

// PostgreSQL SHOW search_path returns an identifier list, not a comma-delimited
// display string. Only doubled quotes escape quotes inside an identifier.
func decodeSearchPath(value string) ([]string, error) {
	if strings.TrimSpace(value) == `""` {
		return []string{""}, nil
	}
	var names []string
	for {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("empty search_path identifier")
		}
		var name string
		if value[0] == '"' {
			var b strings.Builder
			i := 1
			for ; i < len(value); i++ {
				if value[i] != '"' {
					b.WriteByte(value[i])
					continue
				}
				if i+1 < len(value) && value[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				break
			}
			if i == len(value) {
				return nil, errors.New("unclosed search_path identifier")
			}
			name, value = b.String(), strings.TrimSpace(value[i+1:])
			if value != "" && value[0] != ',' {
				return nil, errors.New("ambiguous search_path identifier suffix")
			}
		} else {
			var comma bool
			name, value, comma = strings.Cut(value, ",")
			name = strings.TrimSpace(name)
			// Legacy schema activation also wrote decoded names (including an
			// interior quote). Treat these as data, never executable SQL.
			if comma {
				value = "," + value
			}
		}
		if name == "" {
			return nil, errors.New("empty search_path identifier")
		}
		names = append(names, name)
		if value == "" {
			return names, nil
		}
		value = value[1:]
	}
}

func decodeLegacySetting(key, value string) (common.CanonicalSessionSetting, error) {
	if key == "search_path" {
		names, err := decodeSearchPath(value)
		return decodedSetting(names...), err
	}
	// Old state mixed plain decoded scalars with ordinary SQL string tokens.
	// Escape/dollar strings and expressions cannot be decoded without knowing
	// the original server settings; reject rather than guess their meaning.
	if strings.HasPrefix(value, "'") {
		var b strings.Builder
		for i := 1; i < len(value); i++ {
			if value[i] != '\'' {
				if value[i] == '\\' {
					return common.CanonicalSessionSetting{}, errors.New("ambiguous legacy string escape")
				}
				b.WriteByte(value[i])
				continue
			}
			if i+1 < len(value) && value[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			if i != len(value)-1 {
				return common.CanonicalSessionSetting{}, errors.New("ambiguous legacy string suffix")
			}
			return decodedSetting(b.String()), nil
		}
		return common.CanonicalSessionSetting{}, errors.New("unclosed legacy string")
	}
	if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "$") || strings.HasPrefix(strings.ToLower(value), "e'") || strings.EqualFold(value, "default") || strings.ContainsAny(value, ";()") || strings.Contains(value, "--") || strings.Contains(value, "/*") {
		return common.CanonicalSessionSetting{}, errors.New("ambiguous legacy scalar")
	}
	return decodedSetting(value), nil
}

func encodeSetting(key string, value common.CanonicalSessionSetting) (sql, display string, err error) {
	if value.Provenance != "decoded" || len(value.Values) == 0 || key != "search_path" && len(value.Values) != 1 {
		return "", "", common.ErrInvalidCanonicalSessionSetting
	}
	if key == "search_path" {
		// The version-1 envelope requires a value; one empty string denotes
		// PostgreSQL's valid empty path, not an empty identifier in a list.
		if len(value.Values) == 1 && value.Values[0] == "" {
			return "SET search_path TO ''", "", nil
		}
		var quoted []string
		for _, name := range value.Values {
			if name == "" || strings.ContainsRune(name, 0) {
				return "", "", common.ErrInvalidCanonicalSessionSetting
			}
			quoted = append(quoted, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
		}
		return "SET search_path TO " + strings.Join(quoted, ", "), strings.Join(value.Values, ", "), nil
	}
	display = value.Values[0]
	if strings.ContainsRune(display, 0) {
		return "", "", common.ErrInvalidCanonicalSessionSetting
	}
	if key == "statement_timeout" {
		canon, canonErr := session.CanonicalizeStatementTimeout(display)
		return "SET statement_timeout = '" + canon + "'", display, canonErr
	}
	if !gucAllowlist[key] && key != "role" {
		return "", "", common.ErrInvalidCanonicalSessionSetting
	}
	literal := "'" + strings.ReplaceAll(display, "'", "''") + "'"
	if strings.Contains(display, `\`) {
		literal = "E" + strings.ReplaceAll(literal, `\`, `\\`)
	}
	return "SET " + key + " TO " + literal, display, nil
}

// Read back the effective value rather than persisting SQL tokens (DEFAULT,
// quoted strings and server normalization all have different token meanings).
func readTrackedSetting(ctx context.Context, sess *session.SQLSession, key string) (common.CanonicalSessionSetting, error) {
	if !gucAllowlist[key] && key != "role" {
		return common.CanonicalSessionSetting{}, common.ErrInvalidCanonicalSessionSetting
	}
	res, err := sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "SHOW " + key})
	if err != nil {
		return common.CanonicalSessionSetting{}, err
	}
	if len(res.Rows) != 1 || res.Rows[0] == nil || len(res.Rows[0].Values) != 1 {
		return common.CanonicalSessionSetting{}, errors.New("missing setting readback")
	}
	value, ok := res.Rows[0].Values[0].(string)
	if !ok {
		return common.CanonicalSessionSetting{}, fmt.Errorf("unexpected setting readback type %T", res.Rows[0].Values[0])
	}
	if key == "search_path" {
		if value == "" {
			return decodedSetting(""), nil
		}
		names, decodeErr := decodeSearchPath(value)
		return decodedSetting(names...), decodeErr
	}
	return decodedSetting(value), nil
}
