// Package bot runs one meeting from start to finish: it joins the call,
// records the captions, leaves when the meeting is over and writes the notes.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/meet"
	"github.com/dpage/slonik-bot/internal/output"
	"github.com/dpage/slonik-bot/internal/summary"
	"github.com/dpage/slonik-bot/internal/transcript"
)

// Call is one joined meeting. *meet.Session implements it; tests use fakes.
type Call interface {
	Join(ctx context.Context) error
	StartCaptions(ctx context.Context, language string, events chan<- captions.Event) error
	SendChat(ctx context.Context, msg string) error
	Participants(ctx context.Context) ([]string, error) // other people in the call, not the bot
	Status(ctx context.Context) (meet.Status, error)
	Leave(ctx context.Context) error
	Close() error
}

var _ Call = (*meet.Session)(nil)

// Options configures Run.
type Options struct {
	Config    config.Config
	Code      string
	NoSummary bool
	OutDir    string        // "" means output.Dir(Config.OutputDir, Code, start)
	Poll      time.Duration // 0 means 2s
	Idle      time.Duration // 0 means 4s
	Logger    *slog.Logger  // nil means slog.Default()
}

// Deps are the parts of Run that tests replace.
type Deps struct {
	Dial func(ctx context.Context) (Call, error)
	LLM  summary.LLM // nil when NoSummary
}

// Outcome describes how a run went.
type Outcome struct {
	Dir        string
	Reason     string // "ended", "removed", "alone", "max duration", "interrupted", "browser closed"
	Utterances int
	Summarised bool
}

// Reasons the bot stops recording.
const (
	reasonEnded       = "ended"
	reasonRemoved     = "removed"
	reasonAlone       = "alone"
	reasonMaxDuration = "max duration"
	reasonInterrupted = "interrupted"
	reasonBrowserGone = "browser closed"
)

const (
	defaultPoll  = 2 * time.Second
	defaultIdle  = 4 * time.Second
	leaveTimeout = 10 * time.Second
	eventBuffer  = 256
)

// ErrNoCaptions is returned when the meeting ended without a single caption.
var ErrNoCaptions = errors.New("bot: no captions were captured; check that live captions are available in this meeting")

// Run joins the meeting, records it until it ends and writes the notes. The
// transcript is appended to disk as it is captured, so it survives a crash.
func Run(ctx context.Context, o Options, d Deps) (Outcome, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	poll := o.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	idle := o.Idle
	if idle <= 0 {
		idle = defaultIdle
	}
	cfg := o.Config

	// The disk is checked before Chrome joins anything.
	dir := o.OutDir
	if dir == "" {
		dir = output.Dir(cfg.OutputDir, o.Code, time.Now())
	}
	out := Outcome{Dir: dir}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return out, fmt.Errorf("bot: creating %s: %w", dir, err)
	}
	jsonlPath := filepath.Join(dir, "transcript.jsonl")
	meetingPath := filepath.Join(dir, "meeting.json")
	w, err := transcript.Create(jsonlPath)
	if err != nil {
		return out, fmt.Errorf("bot: %w", err)
	}
	writerOpen := true
	closeWriter := func() {
		if !writerOpen {
			return
		}
		writerOpen = false
		if err := w.Close(); err != nil {
			log.Error("closing the transcript", "path", jsonlPath, "error", err)
		}
	}
	defer closeWriter()

	call, err := d.Dial(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = call.Close() }()
	// Meet's errors already say what to do about them.
	if err := call.Join(ctx); err != nil {
		return out, err
	}
	leave := func() {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaveTimeout)
		defer cancel()
		if err := call.Leave(lctx); err != nil {
			log.Warn("leaving the call", "error", err)
		}
	}

	started := time.Now()
	log.Info("joined the meeting", "code", o.Code, "notes", dir)
	m := transcript.Meeting{Code: o.Code, Started: started}
	// Written now so that a crash mid-meeting still leaves enough for summarise.
	if err := transcript.WriteMeeting(meetingPath, m); err != nil {
		log.Error("writing the meeting details", "path", meetingPath, "error", err)
	}

	events := make(chan captions.Event, eventBuffer)
	if err := call.StartCaptions(ctx, cfg.CaptionLanguage, events); err != nil {
		leave()
		return out, fmt.Errorf("bot: captions: %w", err)
	}
	if err := call.SendChat(ctx, cfg.ChatNotice); err != nil {
		log.Warn("could not post the notice in the chat; carrying on", "error", err)
	}

	var us []transcript.Utterance
	asm := captions.NewAssembler(idle, func(u transcript.Utterance) {
		us = append(us, u)
		if err := w.Append(u); err != nil {
			log.Error("writing to the transcript", "path", jsonlPath, "error", err)
		}
	})

	seen := map[string]bool{}
	reason := watch(ctx, call, cfg, started, poll, events, asm, seen, log)
	log.Info("stopped recording", "reason", reason)
	out.Reason = reason

	// Anything already buffered still belongs to the meeting.
drain:
	for {
		select {
		case ev := <-events:
			asm.Observe(ev, time.Now())
		default:
			break drain
		}
	}
	asm.Flush(time.Now())
	closeWriter()

	switch reason {
	case reasonEnded, reasonRemoved, reasonBrowserGone:
	default:
		leave()
	}

	m.Ended = time.Now()
	for _, s := range transcript.Speakers(us) {
		seen[s] = true
	}
	m.Participants = make([]string, 0, len(seen))
	for name := range seen {
		m.Participants = append(m.Participants, name)
	}
	slices.Sort(m.Participants)
	if err := transcript.WriteMeeting(meetingPath, m); err != nil {
		log.Error("writing the meeting details", "path", meetingPath, "error", err)
	}

	out.Utterances = len(us)
	if len(us) == 0 {
		return out, ErrNoCaptions
	}
	var llm summary.LLM
	if !o.NoSummary {
		llm = d.LLM
	}
	// Ctrl-C ends the meeting, not the summary.
	out.Summarised, err = finish(context.WithoutCancel(ctx), dir, jsonlPath, m, us, llm)
	return out, err
}

// watch feeds caption events to the assembler and polls the call until there
// is a reason to stop, which it returns. It adds every named participant it
// sees to seen.
func watch(ctx context.Context, call Call, cfg config.Config, started time.Time, poll time.Duration,
	events <-chan captions.Event, asm *captions.Assembler, seen map[string]bool, log *slog.Logger) string {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var aloneSince time.Time
	for {
		select {
		case <-ctx.Done():
			return reasonInterrupted
		case ev := <-events:
			asm.Observe(ev, time.Now())
		case now := <-ticker.C:
			asm.Tick(now)

			st, err := call.Status(ctx)
			switch {
			case err != nil && errors.Is(err, context.Canceled):
				// The next pass reports the interruption.
				continue
			case err != nil:
				log.Warn("could not read the meeting page; assuming the browser has closed", "error", err)
				return reasonBrowserGone
			case st == meet.Ended:
				return reasonEnded
			case st == meet.Removed:
				return reasonRemoved
			}

			people, err := call.Participants(ctx)
			if err != nil {
				log.Debug("reading the participants", "error", err)
			} else {
				for _, p := range people {
					if p != "" {
						seen[p] = true
					}
				}
				// Empty strings are people Meet counts but does not name.
				if len(people) == 0 {
					if aloneSince.IsZero() {
						aloneSince = now
					}
				} else {
					aloneSince = time.Time{}
				}
			}
			if !aloneSince.IsZero() && now.Sub(aloneSince) >= cfg.AloneTimeout {
				return reasonAlone
			}
			if now.Sub(started) >= cfg.MaxDuration {
				return reasonMaxDuration
			}
		}
	}
}

// SummariseFile summarises a saved transcript.jsonl, writing the notes to
// outDir, or beside the transcript when outDir is empty.
func SummariseFile(ctx context.Context, jsonl, outDir string, llm summary.LLM) error {
	us, err := transcript.Read(jsonl)
	if err != nil {
		return fmt.Errorf("bot: %w", err)
	}
	if len(us) == 0 {
		return fmt.Errorf("bot: %s holds no utterances", jsonl)
	}
	m, err := transcript.ReadMeeting(filepath.Join(filepath.Dir(jsonl), "meeting.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		m = transcript.Meeting{Code: "unknown", Started: us[0].Start, Ended: us[len(us)-1].End}
	case err != nil:
		return fmt.Errorf("bot: %w", err)
	}
	if outDir == "" {
		outDir = filepath.Dir(jsonl)
	}
	_, err = finish(ctx, outDir, jsonl, m, us, llm)
	return err
}

// finish writes the notes, with a summary when llm is not nil. If the summary
// fails the transcript is still written, and the error says how to retry.
func finish(ctx context.Context, dir, jsonl string, m transcript.Meeting, us []transcript.Utterance, llm summary.LLM) (bool, error) {
	if llm == nil {
		return false, write(dir, m, us, nil)
	}
	r, err := summary.Summarise(ctx, llm, m, us)
	if err != nil {
		if werr := write(dir, m, us, nil); werr != nil {
			return false, errors.Join(werr, fmt.Errorf("bot: summary failed: %w", err))
		}
		return false, fmt.Errorf("bot: summary failed (%w); the transcript is saved, run: slonik-bot summarise %s", err, jsonl)
	}
	if err := write(dir, m, us, r); err != nil {
		return false, err
	}
	return true, nil
}

func write(dir string, m transcript.Meeting, us []transcript.Utterance, r *summary.Result) error {
	if err := output.WriteFiles(dir, m, us, r); err != nil {
		return fmt.Errorf("bot: writing the notes to %s: %w", dir, err)
	}
	return nil
}
