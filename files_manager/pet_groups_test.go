// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"testing"
)

func groupFaceOf(id, person, hash string, named bool, v ...float32) groupFace {
	return groupFace{id: id, person: person, hash: hash, named: named, emb: normalised(v)}
}

// Pets alike on average are one: a dog filed as two, an unnamed pet with a
// named one. Never two in one photo together, nor two with a name, nor two
// unlike each other. An unnamed pet twice in a photo keeps there the face
// most like it, and the other is grouped on its own.
func TestGroupPets(t *testing.T) {
	f := groupFaceOf
	for _, c := range []struct {
		what  string
		faces []groupFace
		want  string
	}{
		{"one dog filed as two", []groupFace{
			f("a1", "A", "h1", false, 1, 0.1, 0), f("a2", "A", "h2", false, 1, 0, 0.1),
			f("b1", "B", "h3", false, 1, 0.2, 0.1), f("b2", "B", "h4", false, 0.9, 0.1, 0.2)}, "[[0 1 2 3]]"},
		{"two dogs alike in one photo together", []groupFace{
			f("a1", "A", "h1", false, 1, 0.1, 0), f("b1", "B", "h1", false, 1, 0, 0.1)}, "[[0] [1]]"},
		{"two named dogs alike", []groupFace{
			f("a1", "A", "h1", true, 1, 0.1, 0), f("b1", "B", "h2", true, 1, 0, 0.1)}, "[[0] [1]]"},
		{"an unnamed dog like a named one", []groupFace{
			f("a1", "A", "h1", false, 1, 0.1, 0), f("b1", "B", "h2", true, 1, 0, 0.1)}, "[[0 1]]"},
		{"two dogs unlike each other", []groupFace{
			f("a1", "A", "h1", false, 1, 0, 0), f("b1", "B", "h2", false, 0, 1, 0)}, "[[0] [1]]"},
		// A's kennel photo h1 has A and a dog like B: that face goes with B.
		{"an unnamed dog twice in a photo", []groupFace{
			f("a1", "A", "h1", false, 1, 0.1, 0), f("a2", "A", "h2", false, 1, 0, 0.1),
			f("a3", "A", "h1", false, 0, 1, 0.1), f("b1", "B", "h3", false, 0.1, 1, 0)}, "[[0 1] [2 3]]"},
		// Average linkage: C is like one of A's photos and unlike the other.
		{"like one photo of a pet but not the pet", []groupFace{
			f("a1", "A", "h1", false, 1, 0, 0), f("a2", "A", "h2", false, 0, 1, 0),
			f("c1", "C", "h3", false, 1, 0.05, 0)}, "[[0 1] [2]]"},
	} {
		if got := fmt.Sprint(groupPets(c.faces, 0.6)); got != c.want {
			t.Errorf("%s: %s, want %s", c.what, got, c.want)
		}
	}
}

// A group is its named pet, or else the pet with most of its faces among
// those mostly there, or else a new one: pets all of whose faces are there
// are merged into it, the faces of pets mostly elsewhere given to it.
func TestPlanGroups(t *testing.T) {
	f := groupFaceOf
	faces := []groupFace{
		f("a1", "A", "h1", false), f("a2", "A", "h2", false), f("a3", "A", "h3", false),
		f("b1", "B", "h4", false), f("n1", "N", "h5", true),
		f("c1", "C", "h6", false), f("c2", "C", "h7", false), f("c3", "C", "h1", false),
	}
	for _, c := range []struct {
		what   string
		groups [][]int
		want   string
	}{
		{"as filed", [][]int{{0, 1, 2}, {3}, {4}, {5, 6, 7}}, "[]"},
		{"B is A", [][]int{{0, 1, 2, 3}, {4}, {5, 6, 7}}, "[{A [B] []}]"},
		{"A is N, the one named", [][]int{{0, 1, 2, 4}, {3}, {5, 6, 7}}, "[{N [A] []}]"},
		{"C's face in A's photo h1 is B", [][]int{{0, 1, 2}, {3, 7}, {4}, {5, 6}}, "[{B [] [c3]}]"},
		{"C's face in A's photo h1 alone", [][]int{{0, 1, 2}, {3}, {4}, {5, 6}, {7}}, "[{ [] [c3]}]"},
	} {
		if got := fmt.Sprint(planGroups(faces, c.groups, nil)); got != c.want {
			t.Errorf("%s: %s, want %s", c.what, got, c.want)
		}
	}
	// B has a cat's face too (the detector took it for a cat once): its dog
	// faces are given to A, B is not merged.
	if got := fmt.Sprint(planGroups(faces, [][]int{{0, 1, 2, 3}, {4}, {5, 6, 7}}, map[string]int{"A": 3, "B": 2, "N": 1, "C": 3})); got != "[{A [] [b1]}]" {
		t.Errorf("a pet with faces of another kind: %s", got)
	}
}
