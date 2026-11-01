// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"sync"

	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
)

// On this day leaves out pictures of documents and screens (memories.go's
// leftOut). Their tags said so, but Image Tagging is off by default on a
// low-memory device since release 158 (tagging_default.go), so Smart
// search's vectors say it too: a photo is document-like by how much closer
// it is to the best of docPrompts than to the best of momentPrompts.
//
// Calibrated on SigLIP 2 base (2026-11-01) with the demo library's 114
// photos against 41 pictures of documents and screens - printed pages,
// a scanned receipt, phone screenshots, and receipts, pages and phones
// laid on the demo's tables, filling the frame or not: every photo scored
// -0.023 or less; every document or screen filling the frame +0.02 or
// more, the receipts +0.06 to +0.10. cDocumentStrong leaves a picture out
// whatever else it is, cDocumentWeak only when nothing says it is a photo
// of a moment (a camera, people), as the weak tags do.
const (
	cDocumentStrong = 0.025
	cDocumentWeak   = 0.0
)

// The prompts are the model's input, in the English it was trained on;
// never shown.
var (
	docPrompts = []string{
		"a screenshot of a phone screen", "a screenshot of a website", "a photo of a receipt", // i18n-ignore: model prompts
		"a scanned document", "a page of printed text", "a printed letter or form", "a restaurant menu", // i18n-ignore: model prompts
		"a whiteboard with writing", "a business card", "a bill or an invoice", "a photo of a computer screen", // i18n-ignore: model prompts
		"a screenshot of a map", "a handwritten note", "a ticket", "a page of a book", // i18n-ignore: model prompts
	}
	momentPrompts = []string{
		"a photo of people", "a family photo", "a photo of a child", "a landscape", "a city street", // i18n-ignore: model prompts
		"a photo of a dog", "food on a table", "a beach", "a party", "a selfie", "mountains", // i18n-ignore: model prompts
		"a garden with flowers", "a room in a house", "a building", "a snowy landscape", "a sunset", // i18n-ignore: model prompts
	}
)

// documentPrompts is the prompts' vectors, made once per process by the
// text tower and kept.
type documentPrompts struct {
	mu          sync.Mutex
	doc, moment [][]float32
}

// vectors is the prompts' vectors, made with embed the first time.
func (d *documentPrompts) vectors(embed func(string) ([]float32, error)) (doc, moment [][]float32, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.doc == nil {
		embedAll := func(prompts []string) ([][]float32, error) {
			out := make([][]float32, 0, len(prompts))
			for _, p := range prompts {
				v, err := embed(p)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			return out, nil
		}
		dv, err := embedAll(docPrompts)
		if err != nil {
			return nil, nil, err
		}
		mv, err := embedAll(momentPrompts)
		if err != nil {
			return nil, nil, err
		}
		d.doc, d.moment = dv, mv
	}
	return d.doc, d.moment, nil
}

// smartDocument is how document-like each photo looks by its vector (see
// above), false for one without a vector; nil while Smart search is off,
// unavailable or its vectors can't be read.
func (mg *Manager) smartDocument(ses *session.Session) func(hash string) (float32, bool) {
	if ses == nil || !mg.SmartSearchEnabled() || mg.smartEmbedder() == nil {
		return nil
	}
	embed := func(text string) ([]float32, error) {
		ctx, cancel := context.WithTimeout(context.Background(), cEmbedTimeout)
		defer cancel()
		return mg.smartEmbedder().EmbedText(ctx, text)
	}
	doc, moment, err := mg.smart.documents.vectors(embed)
	if err != nil {
		log.Debug("smart search: no document check for the memories:", err)
		return nil
	}
	if err := mg.withIndex(ses, func(*smartIndex) {}); err != nil {
		log.Debug("smart search: the descriptions couldn't be read for the memories:", err)
		return nil
	}
	return func(hash string) (float32, bool) {
		var score float32
		var ok bool
		mg.withIndex(ses, func(x *smartIndex) { score, ok = x.documentLocked(hash, doc, moment) })
		return score, ok
	}
}

// documentLocked is hash's best document prompt's cosine less its best
// moment prompt's, false without its vector or with prompts of another
// size. x.mu held.
func (x *smartIndex) documentLocked(hash string, doc, moment [][]float32) (float32, bool) {
	i, ok := x.pos[hash]
	if !ok || len(doc) == 0 || len(moment) == 0 {
		return 0, false
	}
	best := func(prompts [][]float32) (float32, error) {
		var b float32 = -2
		for _, q := range prompts {
			if len(q) != x.dim {
				return 0, errors.New("a prompt of another size") // i18n-ignore: internal
			}
			b = max(b, x.cosineLocked(i, q))
		}
		return b, nil
	}
	d, err := best(doc)
	if err != nil {
		return 0, false
	}
	m, err := best(moment)
	if err != nil {
		return 0, false
	}
	return d - m, true
}
