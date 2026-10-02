package utils

import "testing"

func TestReview_redact_KeyValues(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"no_secret", `host=h user=u`, `host=h user=u`},
		{"non_key", `notpassword=x password_file=p my_sslpassword=q`, `notpassword=x password_file=p my_sslpassword=q`},
		{"bare", `host=h password=REVIEW_SECRET_TAIL user=u`, `host=h password=*** user=u`},
		{"single_quote", `password='head\' REVIEW_SECRET_TAIL' user=u`, `password=*** user=u`},
		{"double_quote", `password="head\" REVIEW_SECRET_TAIL" user=u`, `password=*** user=u`},
		{"double_quote_suffix", `password="head\"inner"REVIEW_SECRET_TAIL user=u`, `password=*** user=u`},
		{"tls_double_quote_suffix", `sslpassword="head"REVIEW_TLS_TAIL user=u`, `sslpassword=*** user=u`},
		{"bare_quote", `password=head'REVIEW_SECRET_TAIL user=u`, `password=*** user=u`},
		{"bare_escaped_quote", `password=head\'REVIEW_SECRET_TAIL user=u`, `password=*** user=u`},
		{"escaped_space", `password=head\ REVIEW_SECRET_TAIL user=u`, `password=*** user=u`},
		{"backslash_before_close", `password='head\\' user=u`, `password=*** user=u`},
		{"backslash_and_quote", `password='head\\\' REVIEW_SECRET_TAIL' user=u`, `password=*** user=u`},
		{"empty", `password='' user=u`, `password=*** user=u`},
		{"empty_at_end", `password=`, `password=***`},
		{"mixed_case", `pAsSwOrD='a' SsLpAsSwOrD='b' user=u`, `pAsSwOrD=*** SsLpAsSwOrD=*** user=u`},
		{"repeated", `password='a' password=b sslpassword='c'`, `password=*** password=*** sslpassword=***`},
		{"spaces", "password \t=\n 'a'\ruser=u", "password \t=\n ***\ruser=u"},
		{"unterminated_single", `error: password='head REVIEW_SECRET_TAIL user=u`, `error: password=***`},
		{"unterminated_double", `error: password="head REVIEW_SECRET_TAIL user=u`, `error: password=***`},
		{"trailing_escape_quoted", `password='head\`, `password=***`},
		{"trailing_escape_bare", `password=head\`, `password=***`},
		{"error_wrapper", "cannot parse `host=h password='a' user=u`: invalid port", "cannot parse `host=h password=*** user=u`: invalid port"},
		{"url_passthrough", `postgres://u:p@h/d`, `postgres://u:p@h/d`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactDSNKeyValues(tc.in)
			if got != tc.want {
				t.Error("redaction mismatch; raw credential-bearing output omitted")
			}
			if RedactDSNKeyValues(got) != got {
				t.Error("redaction is not idempotent")
			}
		})
	}
}
