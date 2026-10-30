// Command slonik-bot joins a Google Meet call, records the live captions and
// writes a summary, decisions and action items when the call ends.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/dpage/slonik-bot/internal/version"
)

const usage = `usage: slonik-bot <command> [flags]

commands:
  login                     sign the bot's Chrome profile in to Google
  join <code-or-url>        join a meeting, transcribe it and summarise it
  summarise <transcript>    re-run the summary for a saved transcript.jsonl
  version                   print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the os.Exit, so tests can drive it.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "slonik-bot %s %s\n", version.Version, version.Commit)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "slonik-bot: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
