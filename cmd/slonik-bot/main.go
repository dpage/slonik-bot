// Command slonik-bot joins a Google Meet call, records the live captions and
// writes a summary, decisions and action items when the call ends.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/dpage/slonik-bot/internal/bot"
	"github.com/dpage/slonik-bot/internal/browser"
	"github.com/dpage/slonik-bot/internal/config"
	"github.com/dpage/slonik-bot/internal/meet"
	"github.com/dpage/slonik-bot/internal/summary"
	"github.com/dpage/slonik-bot/internal/version"
)

const usage = `usage: slonik-bot <command> [flags]

commands:
  login                     sign the bot's Chrome profile in to Google
  join <code-or-url>        join a meeting, transcribe it and summarise it
  summarise <transcript>    re-run the summary for a saved transcript.jsonl
  version                   print the version

Run slonik-bot <command> --help for the command's flags.
`

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const loginURL = "https://accounts.google.com/"

// launchBrowser starts Chrome; tests replace it.
var launchBrowser = browser.Launch

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the os.Exit, so tests can drive it.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "slonik-bot %s %s\n", version.Version, version.Commit)
		return exitOK
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "join":
		return join(args[1:], stdout, stderr)
	case "summarise":
		return summarise(args[1:], stdout, stderr)
	case "login":
		return login(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "slonik-bot: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// common holds the flags every command shares.
type common struct {
	fs      *flag.FlagSet
	config  string
	out     string
	verbose bool
}

func newFlags(name, synopsis string, stderr io.Writer, withOut bool, outHelp string) *common {
	c := &common{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	c.fs.SetOutput(stderr)
	c.fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: slonik-bot %s\n\nflags:\n", synopsis)
		c.fs.PrintDefaults()
	}
	c.fs.StringVar(&c.config, "config", config.DefaultPath(), "configuration file")
	if withOut {
		c.fs.StringVar(&c.out, "out", "", outHelp)
	}
	c.fs.BoolVar(&c.verbose, "verbose", false, "log debugging detail")
	return c
}

// parse parses flags that may come before, between or after the positional
// arguments, and returns the positionals. The int is the exit code to use
// when parsing fails, and -1 when it succeeded.
func (c *common) parse(args []string) ([]string, int) {
	var pos []string
	for {
		if err := c.fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, exitOK
			}
			return nil, exitUsage
		}
		rest := c.fs.Args()
		if len(rest) == 0 {
			return pos, -1
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// set reports whether the named flag was given on the command line.
func (c *common) set(name string) bool {
	found := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// logger sets up logging to stderr. It also becomes the default logger, which
// the config package uses to warn about an API key file others can read.
func (c *common) logger(stderr io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if c.verbose {
		level = slog.LevelDebug
	}
	l := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(l)
	return l
}

func (c *common) load() (config.Config, error) {
	return config.Load(c.config, c.set("config"))
}

// fail reports err and returns the runtime failure exit code. The bot
// package's own prefix is dropped, since "slonik-bot: bot:" says nothing more.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "slonik-bot: %s\n", strings.TrimPrefix(err.Error(), "bot: "))
	return exitFailure
}

// signalContext ends at the first SIGINT or SIGTERM, which stops the meeting
// gracefully. Signal handling is then given back, so that a second one kills
// the process in the usual way; the transcript is already on disk by then.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

func join(args []string, stdout, stderr io.Writer) int {
	c := newFlags("join", "join [flags] <code-or-url>", stderr, true,
		"directory to hold the meeting notes folders (default: output_dir from the config)")
	maxDuration := c.fs.Duration("max-duration", 0, "leave after this long (default: max_duration from the config)")
	noSummary := c.fs.Bool("no-summary", false, "save the transcript without asking a model for a summary")
	headless := c.fs.Bool("headless", false, "run Chrome without a window")
	provider := c.fs.String("llm-provider", "", "summary model provider: anthropic or openai (default: from the config)")
	model := c.fs.String("llm-model", "", "summary model (default: from the config)")
	pos, code := c.parse(args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "slonik-bot: join takes exactly one meeting code or URL")
		c.fs.Usage()
		return exitUsage
	}
	meetingCode, err := meet.ParseCode(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "slonik-bot: %v\n", err)
		return exitUsage
	}
	log := c.logger(stderr)

	cfg, err := c.load()
	if err != nil {
		return fail(stderr, err)
	}
	if c.out != "" {
		cfg.OutputDir = c.out
	}
	if c.set("max-duration") {
		cfg.MaxDuration = *maxDuration
	}
	if *provider != "" {
		cfg.LLM.Provider = *provider
	}
	if *model != "" {
		cfg.LLM.Model = *model
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, err)
	}
	// Checked before Chrome starts, so a missing key costs nothing.
	var llm summary.LLM
	if !*noSummary {
		if llm, err = summary.New(cfg.LLM); err != nil {
			return fail(stderr, err)
		}
	}

	ctx, stop := signalContext()
	defer stop()

	// Chrome outlives the first signal, so that the bot can still leave the
	// call; Close below shuts it down.
	b, err := launchBrowser(context.WithoutCancel(ctx), browser.Options{
		ProfileDir: cfg.ProfileDir,
		ChromePath: cfg.ChromePath,
		Headless:   *headless,
		Logger:     log,
	})
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = b.Close() }()

	dial := func(ctx context.Context) (bot.Call, error) {
		return meet.Dial(ctx, b, meet.DefaultBaseURL, meetingCode, meet.Options{BotName: cfg.BotName, Logger: log})
	}
	out, err := bot.Run(ctx, bot.Options{
		Config:    cfg,
		Code:      meetingCode,
		NoSummary: *noSummary,
		Logger:    log,
	}, bot.Deps{Dial: dial, LLM: llm})
	return joinResult(out, err, stdout, stderr)
}

// joinResult reports how bot.Run went and returns the exit code. Once the
// bot has recorded anything, the meeting folder is named even on failure,
// since the transcript in it is still worth having.
func joinResult(out bot.Outcome, err error, stdout, stderr io.Writer) int {
	if err == nil {
		fmt.Fprintf(stdout, "Notes written to %s (the bot stopped because: %s)\n", out.Dir, out.Reason)
		return exitOK
	}
	if errors.Is(err, meet.ErrNotSignedIn) {
		fmt.Fprintln(stderr, "slonik-bot: the bot's Chrome profile is not signed in to Google; run: slonik-bot login")
		return exitFailure
	}
	code := fail(stderr, err)
	if out.Reason != "" {
		fmt.Fprintf(stderr, "slonik-bot: the meeting folder is %s (the bot stopped because: %s)\n", out.Dir, out.Reason)
	}
	return code
}

func summarise(args []string, stdout, stderr io.Writer) int {
	c := newFlags("summarise", "summarise [flags] <transcript.jsonl>", stderr, true,
		"directory to write the notes to (default: the transcript's directory)")
	provider := c.fs.String("llm-provider", "", "summary model provider: anthropic or openai (default: from the config)")
	model := c.fs.String("llm-model", "", "summary model (default: from the config)")
	pos, code := c.parse(args)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "slonik-bot: summarise takes exactly one transcript.jsonl file")
		c.fs.Usage()
		return exitUsage
	}
	c.logger(stderr)

	cfg, err := c.load()
	if err != nil {
		return fail(stderr, err)
	}
	if *provider != "" {
		cfg.LLM.Provider = *provider
	}
	if *model != "" {
		cfg.LLM.Model = *model
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, err)
	}
	llm, err := summary.New(cfg.LLM)
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()
	if err := bot.SummariseFile(ctx, pos[0], c.out, llm); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Summary written.")
	return exitOK
}

func login(args []string, stdout, stderr io.Writer) int {
	c := newFlags("login", "login [flags]", stderr, false, "")
	// Accepted so that it can be passed alongside join's flags; login always
	// shows the window, because somebody has to sign in.
	c.fs.Bool("headless", false, "ignored: login always shows the Chrome window")
	pos, code := c.parse(args)
	if code >= 0 {
		return code
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "slonik-bot: login takes no arguments")
		c.fs.Usage()
		return exitUsage
	}
	log := c.logger(stderr)

	cfg, err := c.load()
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signalContext()
	defer stop()

	// Always headed: somebody has to type the password. Chrome outlives the
	// signal, as in join: chromedp starts it with exec.CommandContext, so a
	// cancellable context would kill it on Ctrl-C, losing a sign-in whose
	// cookies Chrome had not yet saved. Close asks it to quit instead.
	b, err := launchBrowser(context.WithoutCancel(ctx), browser.Options{ProfileDir: cfg.ProfileDir, ChromePath: cfg.ChromePath, Logger: log})
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = b.Close() }()

	nctx, cancel := context.WithTimeout(b.Context(), time.Minute)
	err = chromedp.Do(nctx, chromedp.Navigate(loginURL))
	cancel()
	if err != nil {
		return fail(stderr, fmt.Errorf("opening %s: %w", loginURL, err))
	}
	// On macOS, closing the last window leaves Chrome running, and Wait only
	// returns once Chrome exits.
	fmt.Fprintln(stdout, "Sign in as the bot's Google account in the Chrome window, then quit Chrome (Cmd-Q on macOS, where closing the window leaves it running) or press Ctrl-C here.")

	closed := make(chan struct{})
	go func() {
		_ = b.Wait()
		close(closed)
	}()
	select {
	case <-closed:
	case <-ctx.Done():
		log.Info("closing Chrome")
	}
	return exitOK
}
