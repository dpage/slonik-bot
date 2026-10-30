package summary

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/dpage/slonik-bot/internal/transcript"
)

//go:embed vocabulary.txt
var vocabularyFile string

// Vocabulary returns the glossary terms in file order, skipping comments and
// blank lines.
func Vocabulary() []string {
	var out []string
	for line := range strings.SplitSeq(vocabularyFile, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

const systemIntro = `You write the minutes of PostgreSQL Europe meetings from an automatic caption transcript.

The transcript comes from live captions, so expect misheard words, especially PostgreSQL and database jargon. Read it with the glossary below in mind and work out what was meant.

Produce:
- title: a short descriptive title for the meeting.
- summary: the discussion in a few paragraphs of Markdown, organised by topic rather than in transcript order. Attribute positions to people by name where that matters. Refer to people by name, or as "they"; never infer anyone's pronouns from their name.
- decisions: each decision the meeting actually reached, one sentence each. Leave the list empty if none were reached.
- action_items: each task someone agreed to do or was asked to do. Set owner only when the transcript names who will do it, or someone volunteers; otherwise null. Never guess an owner. Set due only when a deadline was stated, in the words used; otherwise null.
- corrections: caption mistakes you are confident about, as exact phrases from the transcript ("from") and what was said ("to"), most useful for glossary terms and people's names. Only include phrases that appear verbatim in the transcript.

Write in British English. Do not invent anything that is not supported by the transcript.

Glossary:
`

// BuildPrompt returns the system and user messages. The system prompt is
// fixed text so that providers can cache it; everything that varies between
// meetings goes in the user message.
func BuildPrompt(m transcript.Meeting, us []transcript.Utterance) (system, user string) {
	system = systemIntro + strings.Join(Vocabulary(), "\n")

	people := slices.Clone(m.Participants)
	people = append(people, transcript.Speakers(us)...)
	slices.Sort(people)
	people = slices.Compact(people)

	started, ended := m.Started.UTC(), m.Ended.UTC()
	user = fmt.Sprintf("Meeting code: %s\nDate: %s (%s to %s UTC)\nParticipants: %s\n\nTranscript:\n%s",
		m.Code, started.Format("2006-01-02"), started.Format("15:04"), ended.Format("15:04"),
		strings.Join(people, ", "), transcript.Format(us, m.Started))
	return system, user
}
