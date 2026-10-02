package utils

import "strings"

// RedactDSNKeyValues scrubs password and sslpassword assignments embedded in
// connection strings or error text. Backslashes escape the next byte in both
// quoted and bare values. Unterminated quoted values consume the remainder so
// an uncertain boundary cannot expose a password suffix.
func RedactDSNKeyValues(s string) string {
	var out strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !dsnWordByte(s[i]) {
			i++
			continue
		}
		start := i
		for i < len(s) && dsnWordByte(s[i]) {
			i++
		}
		key := s[start:i]
		if !strings.EqualFold(key, "password") && !strings.EqualFold(key, "sslpassword") {
			continue
		}
		eq := i
		for eq < len(s) && dsnSpaceByte(s[eq]) {
			eq++
		}
		if eq == len(s) || s[eq] != '=' {
			continue
		}
		value := eq + 1
		for value < len(s) && dsnSpaceByte(s[value]) {
			value++
		}
		end := dsnValueEnd(s, value)
		out.WriteString(s[last:value])
		out.WriteString("***")
		last = end
		i = end
	}
	if last == 0 {
		return s
	}
	out.WriteString(s[last:])
	return out.String()
}

func dsnValueEnd(s string, start int) int {
	i := start
	var quote byte
	if i < len(s) && (s[i] == '\'' || s[i] == '"') {
		quote = s[i]
		i++
	}
	for i < len(s) {
		if s[i] == '\\' {
			i++
			if i < len(s) {
				i++
			}
			continue
		}
		if quote != 0 && s[i] == quote {
			if quote == '\'' {
				return i + 1
			}
			// Double quotes are a compatibility form, not libpq delimiters;
			// attached bare bytes can still belong to the password.
			quote = 0
			i++
			continue
		}
		if quote == 0 && dsnSpaceByte(s[i]) {
			return i
		}
		i++
	}
	return len(s)
}

func dsnWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func dsnSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
