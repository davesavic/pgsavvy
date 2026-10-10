package session

import (
	"slices"
	"testing"
)

func TestScrubCredentialEnvPlatformNames(t *testing.T) {
	input := []string{
		"KEEP=ok",
		"PGSAVVY_KEYRING_PASSPHRASE=synthetic-upper",
		"pgsavvy_keyring_passphrase=synthetic-lower",
		"PgSavvy_Keyring_Passphrase=synthetic-mixed",
		"PGSAVVY_KEYRING_PASSPHRASE_EXTRA=keep",
		"OTHER=PGSAVVY_KEYRING_PASSPHRASE=keep",
		"=C:=C:\\fixture",
	}
	original := slices.Clone(input)
	for _, tc := range []struct {
		name            string
		caseInsensitive bool
		want            []string
	}{
		{"posix", false, []string{input[0], input[2], input[3], input[4], input[5], input[6]}},
		{"windows", true, []string{input[0], input[4], input[5], input[6]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubCredentialEnv(input, tc.caseInsensitive)
			if !slices.Equal(got, tc.want) {
				t.Fatal("environment scrub did not respect platform variable-name semantics")
			}
			if !slices.Equal(input, original) {
				t.Fatal("environment scrub mutated its caller's environment")
			}
		})
	}
}
