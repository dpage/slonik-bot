package meet

import (
	"fmt"
	"regexp"
	"strings"
)

var codePattern = regexp.MustCompile(`^[a-z]{3}-[a-z]{4}-[a-z]{3}$`)

// ParseCode returns the meeting code from a bare code such as abc-defg-hij or
// from a meet.google.com URL (with or without /meet/ before the code),
// lowercased.
func ParseCode(s string) (string, error) {
	in := s
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if rest, ok := strings.CutPrefix(s, "meet.google.com/"); ok {
		if i := strings.IndexAny(rest, "?#"); i >= 0 {
			rest = rest[:i]
		}
		// Meet also serves calls at meet.google.com/meet/<code>.
		s = strings.TrimPrefix(strings.TrimSuffix(rest, "/"), "meet/")
	}
	if !codePattern.MatchString(s) {
		return "", fmt.Errorf("meet: not a Google Meet meeting code or URL: %q", in)
	}
	return s, nil
}
