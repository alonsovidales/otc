// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	textreader "github.com/alonsovidales/otc/text_reader"
)

// A search's words and a photo's text meet case and accents aside.
func TestFoldText(t *testing.T) {
	for in, want := range map[string]string{
		"  GTS-04-R ":         "gts-04-r",
		"Café  Straße\nÑandú": "cafe strasse nandu",
		"Œuvre Ærø Łódź":      "oeuvre aero lodz",
		"İstanbul":            "istanbul",
	} {
		if got := foldText(in); got != want {
			t.Errorf("foldText(%q) = %q, want %q", in, got, want)
		}
	}
}

func rank(text, q string) (float64, bool) { return textRank(newFolded(text), newFolded(q)) }

// The number plate the owner searched for, and the rules for words.
func TestTextRank(t *testing.T) {
	for _, c := range []struct {
		text, q string
		want    float64
		ok      bool
	}{
		{"GTS-04-R", "GTS-04-R", cTextRankPhrase, true},
		{"GTS-04-R", "gts-04-r", cTextRankPhrase, true},
		{"GTS-04-R", "04-R", cTextRankPhrase, true}, // "04" and "R" are words of it
		{"Please lower your volume", "lower your", cTextRankPhrase, true},
		{"Please lower your volume", "volume please", cTextRankWords, true},
		{"BUILDERS MERCHANTS", "builder", cTextRankPhrasePart, true}, // 7 letters: part of a word is enough
		{"HOTDOG STAND", "dog", 0, false},                            // too short to be found inside a word
		{"HOT DOG STAND", "dog", cTextRankPhrase, true},
		{"GTS-04-R", "gts04r", cTextRankWordsPart, true}, // punctuation aside
		{"GTS-04-R", "gts 04 r", cTextRankWords, true},
		{"Factura nº 2026/118", "factura 2026", cTextRankWords, true},
		{"Crème brûlée", "creme brulee", cTextRankPhrase, true},
		{"東京駅", "東京", cTextRankPhrasePart, true}, // no spaces between words
		{"STOP", "stop sign", 0, false},
		{"", "stop", 0, false},
	} {
		got, ok := rank(c.text, c.q)
		if ok != c.ok || got != c.want {
			t.Errorf("textRank(%q, %q) = %v %v, want %v %v", c.text, c.q, got, ok, c.want, c.ok)
		}
	}
}

// The text kept is what was read, trimmed, a line each, within the caps.
func TestEncodeLines(t *testing.T) {
	lines := []textreader.Line{
		{Text: " STOP ", Quad: [8]float32{1, 2, 3, 4, 5, 6, 7, 8}},
		{Text: "  "},
		{Text: "Please lower your volume", Quad: [8]float32{-3, 10.6, 20, 10, 20, 20, 0, 20}},
	}
	stored, err := decodeLines(encodeLines(lines))
	if err != nil {
		t.Fatal(err)
	}
	if got := joinLines(stored); got != "STOP\nPlease lower your volume" {
		t.Errorf("joinLines = %q", got)
	}
	if stored[1].Q != [8]int32{0, 10, 20, 10, 20, 20, 0, 20} {
		t.Errorf("corners = %v, clipped at 0 and truncated", stored[1].Q)
	}
	many := make([]textreader.Line, cMaxTextLines+5)
	for i := range many {
		many[i].Text = "line"
	}
	if stored, _ := decodeLines(encodeLines(many)); len(stored) != cMaxTextLines {
		t.Errorf("%d lines kept, want %d", len(stored), cMaxTextLines)
	}
	long := []textreader.Line{{Text: strings.Repeat("a", cMaxPhotoText)}, {Text: "more"}}
	if stored, _ := decodeLines(encodeLines(long)); len(stored) != 1 {
		t.Errorf("%d lines kept past the size cap, want 1", len(stored))
	}
}

// A text search's list: the text matches first, then Smart search's
// photos, each once.
func TestWithTextMatches(t *testing.T) {
	hits := []dao.ListedHash{{Hash: "plate", Rank: [2]float64{cTextRankPhrase}}, {Hash: "both", Rank: [2]float64{cTextRankWordsPart}}}
	list := []dao.ListedHash{{Hash: "van", Rank: [2]float64{0.21}}, {Hash: "both", Rank: [2]float64{0.18}}, {Hash: "road", Rank: [2]float64{0.09}}}
	var got []string
	for _, h := range withTextMatches(hits, list) {
		got = append(got, h.Hash)
	}
	if strings.Join(got, " ") != "plate both van road" {
		t.Errorf("merged list = %v", got)
	}
	if out := withTextMatches(nil, list); len(out) != 3 {
		t.Errorf("without matches the list changed: %v", out)
	}
}

// textManager is a Manager whose Text in photos is on, with these texts
// already in memory.
func textManager(texts map[string]string) *Manager {
	mg := &Manager{}
	mg.texts.loaded.Store(true)
	mg.texts.on.Store(true)
	mg.texts.index.loaded = true
	mg.texts.index.texts = map[string]foldedText{}
	for h, t := range texts {
		mg.texts.index.texts[h] = newFolded(t)
	}
	mg.texts.index.lastUse = time.Now()
	return mg
}

// The matches come best first, ties by content.
func TestTextMatchesOrder(t *testing.T) {
	mg := textManager(map[string]string{
		"b": "the van GTS-04-R",
		"a": "GTS-04-R",
		"c": "gts04r",
		"d": "nothing here",
	})
	var got []string
	for _, h := range mg.textMatches(nil, "GTS-04-R") {
		got = append(got, h.Hash)
	}
	if strings.Join(got, " ") != "a b c" {
		t.Errorf("matches = %v, want a b (whole) then c (punctuation aside)", got)
	}
	mg.texts.on.Store(false)
	if hits := mg.textMatches(nil, "GTS-04-R"); hits != nil {
		t.Errorf("Text in photos off answered %v", hits)
	}
}

// rankByText puts the text matches first and, without Smart search, keeps
// only them.
func TestRankByTextWithoutSmartSearch(t *testing.T) {
	mg := textManager(map[string]string{"plate": "GTS-04-R"})
	mg.smart.loaded.Store(true) // Smart search off
	files := []*pb.File{{Hash: "beach"}, {Hash: "plate"}, {Hash: "dog"}}
	out, err := mg.rankByText(nil, "gts-04-r", files)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Hash != "plate" {
		t.Errorf("rankByText = %v, want only the plate", out)
	}
}

// The browse cut is Smart search's, from its best photo shown: the text
// matches before it are all kept, and a chunk of text matches alone
// leaves it for later.
func TestCutAtKeepsTextMatches(t *testing.T) {
	hashes := []dao.ListedHash{
		{Hash: "plate", Rank: [2]float64{cTextRankPhrase}},
		{Hash: "sign", Rank: [2]float64{cTextRankWords}},
		{Hash: "van", Rank: [2]float64{0.30}},
		{Hash: "car", Rank: [2]float64{0.25}},
		{Hash: "tree", Rank: [2]float64{0.10}},
	}
	files := func(hs ...string) []*pb.File {
		var out []*pb.File
		for _, h := range hs {
			out = append(out, &pb.File{Hash: h})
		}
		return out
	}
	st := &browseState{list: &searchList{hashes: hashes, order: dao.ListText}, at: 2, end: len(hashes)}
	if kept := st.cutAt(files("plate", "sign")); len(kept) != 2 || st.cut {
		t.Fatalf("text matches alone: kept %d, cut %v", len(kept), st.cut)
	}
	st.at = len(hashes)
	kept := st.cutAt(files("plate", "sign", "van", "car", "tree"))
	var got []string
	for _, f := range kept {
		got = append(got, f.Hash)
	}
	// 0.7 of the best cosine (0.30) is 0.21: the tree goes.
	if strings.Join(got, " ") != "plate sign van car" || !st.cut || st.end != 4 {
		t.Errorf("kept %v, cut %v, end %d", got, st.cut, st.end)
	}
}
