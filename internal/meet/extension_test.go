package meet

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The extension's selectors.js repeats some of selectors.go, one key per line,
// and must say the same.
var (
	selString = regexp.MustCompile(`^\s*(\w+):\s*("(?:[^"\\]|\\.)*"),?$`)
	selArray  = regexp.MustCompile(`^\s*(\w+):\s*(\[.*\]),?$`)
)

func TestExtensionSelectorsMatch(t *testing.T) {
	src, err := os.ReadFile("../../extension/selectors.js")
	if err != nil {
		t.Fatal(err)
	}
	strs := map[string]string{
		"selCaptionsOn":         selCaptionsOn,
		"selCaptionsOff":        selCaptionsOff,
		"selCaptionsMenu":       selCaptionsMenu,
		"selCaptionsRegion":     selCaptionsRegion,
		"selChatButton":         selChatButton,
		"selChatInput":          selChatInput,
		"selLeave":              selLeave,
		"selParticipantMenus":   selParticipantMenus,
		"participantMenuPrefix": participantMenuPrefix,
	}
	lists := map[string][]string{
		"textsEnded":   textsEnded,
		"textsRemoved": textsRemoved,
	}

	gotStrs := map[string]string{}
	gotLists := map[string][]string{}
	for line := range strings.Lines(string(src)) {
		line = strings.TrimRight(line, "\n")
		if m := selString.FindStringSubmatch(line); m != nil {
			v, err := strconv.Unquote(m[2])
			if err != nil {
				t.Fatalf("%s: %v", m[1], err)
			}
			gotStrs[m[1]] = v
		} else if m := selArray.FindStringSubmatch(line); m != nil {
			var v []string
			if err := json.Unmarshal([]byte(m[2]), &v); err != nil {
				t.Fatalf("%s: %v", m[1], err)
			}
			gotLists[m[1]] = v
		}
	}

	for k, want := range strs {
		got, ok := gotStrs[k]
		switch {
		case !ok:
			t.Errorf("selectors.js has no %s", k)
		case got != want:
			t.Errorf("selectors.js %s = %q, want %q", k, got, want)
		}
	}
	for k, want := range lists {
		got, ok := gotLists[k]
		switch {
		case !ok:
			t.Errorf("selectors.js has no %s", k)
		case !slices.Equal(got, want):
			t.Errorf("selectors.js %s = %q, want %q", k, got, want)
		}
	}
}
