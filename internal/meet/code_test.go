package meet

import "testing"

func TestParseCode(t *testing.T) {
	good := map[string]string{
		"abc-defg-hij":                                    "abc-defg-hij",
		" ABC-DEFG-HIJ ":                                  "abc-defg-hij",
		"https://meet.google.com/abc-defg-hij":            "abc-defg-hij",
		"https://meet.google.com/abc-defg-hij/":           "abc-defg-hij",
		"https://meet.google.com/abc-defg-hij?authuser=1": "abc-defg-hij",
		"meet.google.com/abc-defg-hij":                    "abc-defg-hij",
	}
	for in, want := range good {
		if got, err := ParseCode(in); err != nil || got != want {
			t.Errorf("ParseCode(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc-defg", "abcdefghij", "abc-defg-hij-k", "https://example.com/abc-defg-hij", "https://meet.google.com/", "not-a-meeting", "ab1-defg-hij"} {
		if _, err := ParseCode(in); err == nil {
			t.Errorf("ParseCode(%q) accepted", in)
		}
	}
}
