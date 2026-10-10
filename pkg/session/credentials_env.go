package session

import "strings"

func scrubCredentialEnv(env []string, caseInsensitive bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, hasValue := strings.Cut(kv, "=")
		if hasValue && (name == keyringPassphraseEnv || caseInsensitive && strings.EqualFold(name, keyringPassphraseEnv)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
