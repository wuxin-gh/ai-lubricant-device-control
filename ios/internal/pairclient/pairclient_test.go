package pairclient

import "testing"

// Pairing-code normalization must match the server's NormalizeCode exactly, or
// a code the server would accept gets rejected locally (or vice versa). Ported
// from PairingClientNormalizationTest.kt.
func TestNormalizeCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abcd-efgh", "ABCDEFGH"},
		{"ABCDEFGH", "ABCDEFGH"},
		{"abcdefgh", "ABCDEFGH"},
		{"aB-cD_12.34", "ABCD1234"},
		{"  ab cd  ", "ABCD"},
		{"", ""},
		{"---", ""},
		// Only ASCII A-Z0-9 survive; a Cyrillic 'А' is NOT treated as 'A'.
		{"АБВГ", ""},
	}
	for _, c := range cases {
		if got := NormalizeCode(c.in); got != c.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
