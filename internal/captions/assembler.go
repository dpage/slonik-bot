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
}

// Assembler holds the caption blocks still on screen and emits each as an
// utterance once Meet removes it, it goes quiet, or the speaker moves on.
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
		b.text = merged
		b.end = now
	}
}

// Tick finalises blocks whose text has not changed for at least the idle
// period.
func (a *Assembler) Tick(now time.Time) {
	for _, id := range a.ids() {
		if now.Sub(a.blocks[id].end) >= a.idle {
			a.finalise(id)
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

// finalise emits a block and forgets it, so that a later event reusing the id
// starts a new utterance rather than rewriting one already emitted.
func (a *Assembler) finalise(id int) {
	b := a.blocks[id]
	delete(a.blocks, id)
	if text := strings.TrimSpace(b.text); text != "" {
		a.emit(transcript.Utterance{Speaker: b.speaker, Start: b.start, End: b.end, Text: text})
	}
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

	// A match is either the full anchor, or a shorter overlap that runs to
	// the end of held, which is where incoming continues from a tail of only
	// a word or two. Preferring the longest overlap stops repeated phrases
	// matching too early and cutting off speech.
	anchor := min(3, len(inWords))
	tail := min(2, len(inWords))
	best, bestLen := -1, 0
	for j := range heldNorm {
		n := 0
		for j+n < len(heldNorm) && n < len(inNorm) && heldNorm[j+n] == inNorm[n] {
			n++
		}
		if (n >= anchor || (j+n == len(heldNorm) && n >= tail)) && n > bestLen {
			best, bestLen = j, n
		}
	}
	if best >= 0 {
		return strings.Join(append(heldWords[:best:best], inWords...), " ")
	}

	// A short block that no longer matches was most likely re-recognised
	// from its first word; for a long one, a duplicated phrase is better
	// than lost speech.
	if len(heldWords) <= 8 {
		return strings.Join(inWords, " ")
	}
	return strings.Join(append(heldWords, inWords...), " ")
}
