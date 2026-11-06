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
	"time"

	"github.com/dpage/slonik-bot/internal/captions"
	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/meet"
	"github.com/dpage/slonik-bot/internal/output"
	"github.com/dpage/slonik-bot/internal/record"
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
	OutDir    string        // "" means a new folder from output.NewDir(Config.OutputDir, Code, start)
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
	// pollTimeoutFactor bounds one Status or Participants call, in poll
	// intervals.
	pollTimeoutFactor = 3
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
	var err error
	if dir == "" {
		// A new folder for every session, so that re-joining a meeting after
		// a crash or an early exit cannot replace the earlier notes.
		if dir, err = output.NewDir(cfg.OutputDir, o.Code, time.Now()); err != nil {
			return Outcome{Dir: dir}, fmt.Errorf("bot: %w", err)
		}
	}
	out := Outcome{Dir: dir}
	rec, err := record.Start(dir, o.Code, time.Now(), idle, log)
	if err != nil {
		return out, fmt.Errorf("bot: %w", err)
	}
	defer rec.Close()
	// discard removes the folder of a session that never got into the call,
	// so that the next attempt is not pushed on to a -2 name for nothing.
	discard := func() {
		if o.OutDir != "" {
			rec.Close()
			return
		}
		rec.Discard()
	}

	// A Ctrl-C before the bot is in the call (often whilst it waits in the
	// lobby) surfaces from chromedp as a bare "context canceled".
	errInterrupted := errors.New("bot: interrupted before joining")
	call, err := d.Dial(ctx)
	if err != nil {
		discard()
		if ctx.Err() != nil {
			return out, errInterrupted
		}
		return out, err
	}
	defer func() { _ = call.Close() }()
	// Meet's errors already say what to do about them.
	if err := call.Join(ctx); err != nil {
		discard()
		if ctx.Err() != nil {
			return out, errInterrupted
		}
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
	// Written now so that a crash mid-meeting still leaves enough for summarise.
	rec.SetStarted(started)

	events := make(chan captions.Event, eventBuffer)
	if err := call.StartCaptions(ctx, cfg.CaptionLanguage, events); err != nil {
		leave()
		return out, fmt.Errorf("bot: captions: %w", err)
	}
	if err := call.SendChat(ctx, cfg.ChatNotice); err != nil {
		log.Warn("could not post the notice in the chat; carrying on", "error", err)
	}

	reason := watch(ctx, call, cfg, started, poll, events, rec, log)
	log.Info("stopped recording", "reason", reason)
	out.Reason = reason

	// Anything already buffered still belongs to the meeting.
drain:
	for {
		select {
		case ev := <-events:
			rec.Observe(ev, time.Now())
		default:
			break drain
		}
	}
	m, us := rec.Stop(time.Now())

	switch reason {
	case reasonEnded, reasonRemoved, reasonBrowserGone:
	default:
		leave()
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
	out.Summarised, err = record.Finish(context.WithoutCancel(ctx), dir, rec.JSONL(), m, us, llm)
	return out, err
}

// watch feeds caption events to the assembler and polls the call until there
// is a reason to stop, which it returns. It adds every named participant it
// sees to rec.
func watch(ctx context.Context, call Call, cfg config.Config, started time.Time, poll time.Duration,
	events <-chan captions.Event, rec *record.Recording, log *slog.Logger) string {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var aloneSince time.Time
	listed := false // whether the first list of participants has been logged
	for {
		select {
		case <-ctx.Done():
			return reasonInterrupted
		case ev := <-events:
			rec.Observe(ev, time.Now())
		case now := <-ticker.C:
			rec.Tick(now)

			// Each poll is bounded, so that a page which has stopped
			// answering cannot hold up the alone and max duration checks.
			pctx, cancel := context.WithTimeout(ctx, poll*pollTimeoutFactor)
			st, err := call.Status(pctx)
			// chromedp reports a lost browser as an error wrapping
			// context.Canceled, so the contexts decide what an error
			// means, not its type.
			slow := err != nil && pctx.Err() != nil
			cancel()
			switch {
			case err != nil && ctx.Err() != nil:
				return reasonInterrupted
			case slow:
				log.Debug("the meeting page did not answer in time", "error", err)
			case err != nil:
				log.Warn("could not read the meeting page; assuming the browser has closed", "error", err)
				return reasonBrowserGone
			case st == meet.Ended:
				return reasonEnded
			case st == meet.Removed:
				return reasonRemoved
			}

			var people []string
			if !slow {
				pctx, cancel := context.WithTimeout(ctx, poll*pollTimeoutFactor)
				people, err = call.Participants(pctx)
				cancel()
			}
			switch {
			case slow:
			case err != nil:
				log.Debug("reading the participants", "error", err)
			default:
				var named []string
				for _, p := range people {
					if p != "" {
						named = append(named, p)
					}
				}
				rec.AddParticipants(named...)
				if !listed {
					// The bot leaves its own tile out only when bot_name is
					// the account's display name exactly; if it is not, the
					// bot's own name shows up here and it is never alone.
					log.Info("people in the call besides the bot", "count", len(people), "named", named, "bot_name", cfg.BotName)
					listed = true
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
	_, err = record.Finish(ctx, outDir, jsonl, m, us, llm)
	return err
}
