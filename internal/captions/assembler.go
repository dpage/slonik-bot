// Package captions turns Meet's live caption updates into finished utterances.
package captions

import (
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/dpage/slonik-bot/internal/transcript"
)

// Event is one report from observer.js: the current text of a caption block,
// or notice that the block has left the page.
type Event struct {
	Block   int    `json:"block"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Removed bool   `json:"removed"`
}

type block struct {
	speaker    string
	start, end time.Time
	text       string
	sent       int  // words of text already emitted
	quiet      bool // emitted on going quiet; the next change starts a new utterance
}

// Assembler holds the caption blocks still on screen and emits each as an
// utterance once Meet removes it, it goes quiet, or the speaker moves on. A
// block that goes quiet stays open, because Meet carries on adding to it when
// the same speaker resumes; only the words added since are emitted then.
// It is not safe for concurrent use.
type Assembler struct {
	idle   time.Duration
	emit   func(transcript.Utterance)
	blocks map[int]*block
}

// NewAssembler returns an assembler that finalises a block once its text has
// not changed for idle, passing each finished utterance to emit.
func NewAssembler(idle time.Duration, emit func(transcript.Utterance)) *Assembler {
	return &Assembler{idle: idle, emit: emit, blocks: map[int]*block{}}
}

// Observe applies one event received at now.
func (a *Assembler) Observe(ev Event, now time.Time) {
	b, ok := a.blocks[ev.Block]
	if ev.Removed {
		if ok {
			a.finalise(ev.Block)
		}
		return
	}
	if !ok {
		// Meet starts a fresh block when someone resumes after another
		// speaker, so an older open block of theirs is finished.
		if ev.Speaker != "" {
			for _, id := range a.ids() {
				if a.blocks[id].speaker == ev.Speaker {
					a.finalise(id)
				}
			}
		}
		b = &block{speaker: ev.Speaker, start: now, end: now}
		a.blocks[ev.Block] = b
	}
	if ev.Speaker != "" {
		b.speaker = ev.Speaker
	}
	if merged := mergeText(b.text, ev.Text); merged != b.text {
		if b.quiet {
			b.quiet = false
			b.start = now
		}
		b.text = merged
		b.end = now
	}
}

// Tick emits blocks whose text has not changed for at least the idle period.
func (a *Assembler) Tick(now time.Time) {
	for _, id := range a.ids() {
		if b := a.blocks[id]; !b.quiet && now.Sub(b.end) >= a.idle {
			a.send(b)
			b.quiet = true
		}
	}
}

// Flush finalises every open block, in block order.
func (a *Assembler) Flush(now time.Time) {
	for _, id := range a.ids() {
		a.finalise(id)
	}
}

// ids returns the open block ids in ascending order, so that output does not
// depend on map iteration order.
func (a *Assembler) ids() []int {
	ids := make([]int, 0, len(a.blocks))
	for id := range a.blocks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// finalise emits what is left of a block and forgets it, so that a later
// event reusing the id starts a new utterance rather than rewriting one
// already emitted.
func (a *Assembler) finalise(id int) {
	b := a.blocks[id]
	delete(a.blocks, id)
	if !b.quiet {
		a.send(b)
	}
}

// send emits the words of a block not emitted before. Meet may revise words
// already emitted, so this counts words rather than matching them; a revision
// that changes the count can cost or repeat a word at the join.
func (a *Assembler) send(b *block) {
	words := strings.Fields(b.text)
	if len(words) <= b.sent {
		return
	}
	a.emit(transcript.Utterance{Speaker: b.speaker, Start: b.start, End: b.end, Text: strings.Join(words[b.sent:], " ")})
	b.sent = len(words)
}

func normalise(w string) string {
	return strings.ToLower(strings.TrimFunc(w, unicode.IsPunct))
}

// mergeText combines the text held for a block with Meet's latest rendering
// of it. Meet rewrites recent words as recognition improves and drops the
// start of long blocks, so incoming is aligned against held by its first few
// words rather than trusted outright.
func mergeText(held, incoming string) string {
	heldWords := strings.Fields(held)
	inWords := strings.Fields(incoming)
	if len(heldWords) == 0 {
		return strings.Join(inWords, " ")
	}
	if len(inWords) == 0 {
		// A blank update carries nothing to align, and taking it would
		// throw away speech already held.
		return strings.Join(heldWords, " ")
	}

	heldNorm := make([]string, len(heldWords))
	for i, w := range heldWords {
		heldNorm[i] = normalise(w)
	}
	inNorm := make([]string, len(inWords))
	for i, w := range inWords {
		inNorm[i] = normalise(w)
	}

	if j, ok := findOverlap(heldNorm, inNorm, 0, false); ok {
		return strings.Join(append(heldWords[:j:j], inWords...), " ")
	}

	// Meet may have re-recognised the first words of incoming, which then
	// no longer match what is held. Skip up to three of them and align on
	// the rest; the skipped words are taken as revisions of the held words
	// just before the match, so they replace those rather than being lost
	// or duplicated. The remainder must carry a full three-word anchor, as a
	// shorter one could match anywhere and discard the held text after it.
	for k := 1; k <= 3 && len(inNorm)-k >= 3; k++ {
		if j, ok := findOverlap(heldNorm, inNorm[k:], k, true); ok {
			return strings.Join(append(heldWords[:j-k:j-k], inWords...), " ")
		}
	}

	// A short block that no longer matches was most likely re-recognised
	// from its first word; for a long one, a duplicated phrase is better
	// than lost speech.
	if len(heldWords) <= 8 {
		return strings.Join(inWords, " ")
	}
	return strings.Join(append(heldWords, inWords...), " ")
}

// findOverlap looks for where in held the words of in begin. A match is
// either the full anchor, or a shorter overlap that runs to the end of held,
// which is where in continues from a tail of only a word or two. The longest
// overlap wins, which stops repeated phrases matching too early and cutting
// off speech. On equal length a clean continuation (one reaching the end of
// held) beats one that diverges, since choosing the divergent one discards
// the held words after it; remaining ties go to the earliest position.
// Positions before minJ are ignored, and strict drops the short-tail rule so
// only a full anchor qualifies.
func findOverlap(held, in []string, minJ int, strict bool) (int, bool) {
	anchor := min(3, len(in))
	tail := min(2, len(in))
	best, bestLen, bestClean := -1, 0, false
	for j := minJ; j < len(held); j++ {
		n := 0
		for j+n < len(held) && n < len(in) && held[j+n] == in[n] {
			n++
		}
		clean := j+n == len(held)
		if n < anchor && (strict || !clean || n < tail) {
			continue
		}
		if n > bestLen || (n == bestLen && clean && !bestClean) {
			best, bestLen, bestClean = j, n, clean
		}
	}
	return best, best >= 0
}
