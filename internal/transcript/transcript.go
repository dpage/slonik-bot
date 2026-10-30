// Package transcript holds the utterance types and the on-disk transcript.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Utterance is one finished caption: who said it, when, and what.
type Utterance struct {
	Speaker string    `json:"speaker"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Text    string    `json:"text"`
}

// Meeting is the metadata saved beside the transcript, so that a later
// `summarise` run does not need the call to still be around.
type Meeting struct {
	Code         string    `json:"code"`
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended"`
	Participants []string  `json:"participants"`
}

// Correction replaces a mis-heard phrase with the intended one.
type Correction struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Writer appends utterances to a JSON Lines file.
type Writer struct{ f *os.File }

// Create opens path for appending, creating it if needed.
func Create(path string) (*Writer, error) {
	// Re-joining the same meeting reuses the file. If the last run died
	// mid-write, drop the fragment, since appending after it would put two
	// records on one line and make the file unreadable.
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
		if err := os.Truncate(path, int64(bytes.LastIndexByte(b, '\n')+1)); err != nil {
			return nil, fmt.Errorf("transcript: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	return &Writer{f: f}, nil
}

// Append writes one utterance and syncs it to disk. The sync is per line so a
// crash or a killed process loses at most the utterance being written, which
// matters more than throughput at the pace of human speech.
func (w *Writer) Append(u Utterance) error {
	b, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	return nil
}

// Close closes the file.
func (w *Writer) Close() error { return w.f.Close() }

// Read loads a transcript. A final line that does not parse is assumed to be
// a write cut short by a crash and is dropped; a bad line anywhere else means
// the file is damaged, which is an error.
func Read(path string) ([]Utterance, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var (
		out    []Utterance
		n      int
		badAt  int
		badErr error
	)
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if badErr != nil {
			return nil, fmt.Errorf("transcript: %s line %d: %w", path, badAt, badErr)
		}
		var u Utterance
		if err := json.Unmarshal([]byte(line), &u); err != nil {
			badAt, badErr = n, err
			continue
		}
		out = append(out, u)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("transcript: %s: %w", path, err)
	}
	return out, nil
}

// WriteMeeting saves the meeting metadata as indented JSON.
func WriteMeeting(path string, m Meeting) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	return nil
}

// ReadMeeting loads metadata written by WriteMeeting.
func ReadMeeting(path string) (Meeting, error) {
	var m Meeting
	b, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("transcript: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("transcript: %s: %w", path, err)
	}
	return m, nil
}

// Format renders utterances as "[HH:MM:SS] Speaker: text" lines, with times
// counted from origin (normally the moment the bot joined).
func Format(us []Utterance, origin time.Time) string {
	var b strings.Builder
	for _, u := range us {
		d := max(u.Start.Sub(origin), 0)
		secs := int(d.Seconds())
		fmt.Fprintf(&b, "[%02d:%02d:%02d] %s: %s\n", secs/3600, secs/60%60, secs%60, u.Speaker, u.Text)
	}
	return b.String()
}

// Merge joins consecutive utterances by the same speaker. Meet breaks one
// person's speech into many short captions, which read badly line by line.
func Merge(us []Utterance) []Utterance {
	var out []Utterance
	for _, u := range us {
		if n := len(out); n > 0 && out[n-1].Speaker == u.Speaker {
			out[n-1].Text += " " + u.Text
			out[n-1].End = u.End
			continue
		}
		out = append(out, u)
	}
	return out
}

// Speakers lists each distinct speaker once, in order of first appearance.
func Speakers(us []Utterance) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range us {
		if !seen[u.Speaker] {
			seen[u.Speaker] = true
			out = append(out, u.Speaker)
		}
	}
	return out
}

// ApplyCorrections replaces whole-word, case-sensitive occurrences of each
// correction's From text. Matching whole words stops "PG dump" rewriting the
// start of "PG dumper".
func ApplyCorrections(text string, cs []Correction) string {
	for _, c := range cs {
		if c.From != "" {
			text = replaceWhole(text, c.From, c.To)
		}
	}
	return text
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// replaceWhole is a single left-to-right pass that tests the boundaries
// without consuming them, so back-to-back matches sharing a space both
// match, and a To containing From cannot cause a loop.
func replaceWhole(text, from, to string) string {
	var b strings.Builder
	pos := 0
	for {
		i := strings.Index(text[pos:], from)
		if i < 0 {
			break
		}
		i += pos
		end := i + len(from)
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (i == 0 || !isWordRune(before)) && (end == len(text) || !isWordRune(after)) {
			b.WriteString(text[pos:i])
			b.WriteString(to)
			pos = end
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		b.WriteString(text[pos : i+size])
		pos = i + size
	}
	b.WriteString(text[pos:])
	return b.String()
}
