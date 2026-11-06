// Package record writes one meeting's captions to disk as they arrive and
// turns them into notes when the meeting is over. It is shared by the bot
// that joins a call and by the server that receives captions from a browser.
package record

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/output"
	"github.com/dpage/slonik-bot/internal/summary"
	"github.com/dpage/slonik-bot/internal/transcript"
)

// Recording is one meeting being written to dir. It is not safe for
// concurrent use; callers serialise.
type Recording struct {
	dir, jsonl, meetingPath string
	log                     *slog.Logger
	w                       *transcript.Writer
	writerOpen              bool
	asm                     *captions.Assembler
	us                      []transcript.Utterance
	seen                    map[string]bool
	m                       transcript.Meeting
	stopped                 bool
}

// Start creates dir if needed, opens dir/transcript.jsonl and writes
// dir/meeting.json straight away, so that a crash mid-meeting still leaves
// enough for summarise. A nil log means slog.Default().
func Start(dir, code string, started time.Time, idle time.Duration, log *slog.Logger) (*Recording, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("record: creating %s: %w", dir, err)
	}
	r := &Recording{
		dir:         dir,
		jsonl:       filepath.Join(dir, "transcript.jsonl"),
		meetingPath: filepath.Join(dir, "meeting.json"),
		log:         log,
		seen:        map[string]bool{},
		m:           transcript.Meeting{Code: code, Started: started},
	}
	w, err := transcript.Create(r.jsonl)
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	r.w = w
	r.writerOpen = true
	r.asm = captions.NewAssembler(idle, func(u transcript.Utterance) {
		r.us = append(r.us, u)
		if err := r.w.Append(u); err != nil {
			r.log.Error("writing to the transcript", "path", r.jsonl, "error", err)
		}
	})
	r.writeMeeting()
	return r, nil
}

func (r *Recording) writeMeeting() {
	if err := transcript.WriteMeeting(r.meetingPath, r.m); err != nil {
		r.log.Error("writing the meeting details", "path", r.meetingPath, "error", err)
	}
}

func (r *Recording) closeWriter() {
	if !r.writerOpen {
		return
	}
	r.writerOpen = false
	if err := r.w.Close(); err != nil {
		r.log.Error("closing the transcript", "path", r.jsonl, "error", err)
	}
}

// SetStarted changes the start time, for a caller that opens the recording
// before the meeting itself begins (the bot checks the disk before it joins).
func (r *Recording) SetStarted(started time.Time) {
	r.m.Started = started
	r.writeMeeting()
}

// Observe feeds one caption event to the assembler.
func (r *Recording) Observe(ev captions.Event, at time.Time) { r.asm.Observe(ev, at) }

// Tick lets the assembler finish speech that has gone quiet.
func (r *Recording) Tick(now time.Time) { r.asm.Tick(now) }

// AddParticipants notes people seen in the call. Empty names are ignored,
// since they are people Meet counts but does not name.
func (r *Recording) AddParticipants(names ...string) {
	for _, n := range names {
		if n != "" {
			r.seen[n] = true
		}
	}
}

// Stop flushes the assembler, closes the transcript, merges the speakers into
// the participants and rewrites meeting.json. A second call returns the same
// values and does nothing more.
func (r *Recording) Stop(ended time.Time) (transcript.Meeting, []transcript.Utterance) {
	if r.stopped {
		return r.m, r.us
	}
	r.stopped = true
	r.asm.Flush(ended)
	r.closeWriter()

	r.m.Ended = ended
	for _, s := range transcript.Speakers(r.us) {
		r.seen[s] = true
	}
	r.m.Participants = make([]string, 0, len(r.seen))
	for name := range r.seen {
		r.m.Participants = append(r.m.Participants, name)
	}
	slices.Sort(r.m.Participants)
	r.writeMeeting()
	return r.m, r.us
}

// Discard removes the folder of a recording that never got going, so that the
// next attempt is not pushed on to a -2 name for nothing. os.Remove leaves
// anything that has other content in it.
func (r *Recording) Discard() {
	r.closeWriter()
	if fi, err := os.Stat(r.jsonl); err == nil && fi.Size() == 0 {
		_ = os.Remove(r.jsonl)
	}
	_ = os.Remove(r.meetingPath)
	_ = os.Remove(r.dir)
}

// Close closes the transcript without touching anything else. It is safe to
// call more than once, and after Stop.
func (r *Recording) Close() { r.closeWriter() }

// Dir is the folder the recording is written to.
func (r *Recording) Dir() string { return r.dir }

// JSONL is the path of the transcript being appended to.
func (r *Recording) JSONL() string { return r.jsonl }

// Utterances is how many utterances have been finished so far.
func (r *Recording) Utterances() int { return len(r.us) }

// Finish writes the notes, with a summary when llm is not nil. If the summary
// fails the transcript is still written, and the error says how to retry.
func Finish(ctx context.Context, dir, jsonl string, m transcript.Meeting, us []transcript.Utterance, llm summary.LLM) (bool, error) {
	// Written before the model is asked, because that can take minutes and
	// the process can be killed meanwhile (a second Ctrl-C, or docker stop
	// running out of time); the summary then rewrites it with corrections.
	preErr := write(dir, m, us, nil)
	if llm == nil {
		return false, preErr
	}
	r, err := summary.Summarise(ctx, llm, m, us)
	if err != nil {
		retry := "slonik-bot summarise " + shellQuote(jsonl)
		if filepath.Clean(dir) != filepath.Dir(jsonl) {
			retry = "slonik-bot summarise --out " + shellQuote(dir) + " " + shellQuote(jsonl)
		}
		werr := preErr
		if werr != nil {
			werr = write(dir, m, us, nil) // once more, in case it was passing
		}
		if werr != nil {
			// transcript.jsonl is still intact, so the retry is worth giving.
			return false, errors.Join(werr, fmt.Errorf("bot: summary failed (%w); once the notes can be written, run: %s", err, retry))
		}
		return false, fmt.Errorf("bot: summary failed (%w); the transcript is saved, run: %s", err, retry)
	}
	if err := write(dir, m, us, r); err != nil {
		return false, err
	}
	return true, nil
}

// shellQuote makes s one word for a POSIX shell, so that the printed retry
// command can be pasted as is. Plain paths are left alone for readability; the
// rest are single-quoted, which leaves nothing inside special except the
// single quote itself, which is written by closing the quotes, adding an
// escaped quote and reopening them. Any ~ or = is quoted too, since shells
// expand ~ at the start of a word or after = in assignment-like words, and
// zsh expands a leading = as a command path.
func shellQuote(s string) string {
	unsafe := func(c rune) bool {
		safe := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_./-:+,@", c)
		return !safe
	}
	if s != "" && !strings.ContainsFunc(s, unsafe) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func write(dir string, m transcript.Meeting, us []transcript.Utterance, r *summary.Result) error {
	if err := output.WriteFiles(dir, m, us, r); err != nil {
		return fmt.Errorf("bot: writing the notes to %s: %w", dir, err)
	}
	return nil
}
