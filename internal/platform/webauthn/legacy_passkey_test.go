package webauthn

import "testing"

func TestIsLegacyRustPasskey(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "webauthn-rs Passkey shape (top-level cred)",
			raw:  `{"cred":{"cred_id":"AAAA","cred":{"type_":-7,"key":{"EC_EC2":{"curve":"SECP256R1","x":"AA","y":"BB"}}},"counter":5,"user_verified":true}}`,
			want: true,
		},
		{
			name: "go-webauthn Credential shape (id + publicKey)",
			raw:  `{"id":"AAAA","publicKey":"BBBB","flags":{},"authenticator":{"signCount":5}}`,
			want: false,
		},
		{
			name: "go-webauthn with no publicKey but has id",
			raw:  `{"id":"AAAA","flags":{}}`,
			want: false,
		},
		{
			name: "not JSON",
			raw:  `not-json`,
			want: false,
		},
		{
			name: "empty object",
			raw:  `{}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLegacyRustPasskey([]byte(tc.raw)); got != tc.want {
				t.Fatalf("isLegacyRustPasskey(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
