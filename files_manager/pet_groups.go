// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"sort"

	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
)

// Pets are filed one photo at a time, each as the pet it is most like
// (filePets), and a pet's photos are far apart: lying on the sofa, running
// on the beach, wet, the puppy and the dog it grew into. A photo unlike a
// pet's references so far started a pet of its own, and those grew apart
// from each other: on the owner's library one dog was a dozen pets of 20
// to 300 photos (owner, 2026-11-12: "The same pet is identified as
// separate ones everywhere"). So the pets of each kind are grouped again
// (regroupPetsLocked): two pets whose photos are, on average, at least
// petGroupAt alike are one, and become one as a merge in People makes them -
// the closest two first, again and again, as long as two are that alike.
// Two pets in one photo together are never one (one animal is never twice
// in a photo), nor two the owner named (who they named stays as they named
// it); a pet with no name joins a named one. A pet with no name that is
// twice in one photo - two dogs side by side in a kennel, filed as one
// before release 181 - keeps there the face most like the rest of it, and
// each other face starts a pet of its own, to be grouped like any.
//
// Grouping compares pets, not faces: the sum of a pet's embeddings says
// how alike its photos are to another's on average (the dot product of
// two sums over the product of their counts), so a library of a few
// thousand pet photos is grouped in a few seconds on a Pi. It runs after
// the background pass looked at photos for pets (BackfillVision, which
// also runs it once after each start, for what an update changed), and in
// the slow lane after cPetRegroupEvery pets were filed since it last did.

const (
	// cPetRegroupEvery: pets filed by the slow lane since the last
	// grouping that make it group them again.
	cPetRegroupEvery = 25
	// cPetGroupMax: the most pets of one kind grouped at once - each pair's
	// dot product and whether they may be one are kept, 5 bytes a pair (45
	// MB at the most). More are left as filed, and logged.
	cPetGroupMax = 3000
)

// groupFace is a pet face being grouped: whose it is, whether they have a
// name, its photo and its embedding.
type groupFace struct {
	id, person, hash string
	named            bool
	emb              []float32
}

// petGroup is a pet while grouping: its faces (indexes into the faces
// grouped), the sum of their embeddings and the photos they are in (as
// first grouped, before any merge), the person whose name it has ("" none)
// and whether it is still a pet of its own.
type petGroup struct {
	faces  []int
	sum    []float64
	photos map[string]bool
	named  string
	alive  bool
}

// groupPets groups faces, all of one kind (see above), as pets at least
// least alike: each group is the indexes of one pet's faces, in faces'
// order, the groups in the order of their first face. The pets are first
// as filed - a person's faces together - but for an unnamed one's faces in
// a photo where it has one more like the rest of it.
func groupPets(faces []groupFace, least float32) [][]int {
	byPerson := map[string][]int{}
	var order []string
	for i, f := range faces {
		if _, ok := byPerson[f.person]; !ok {
			order = append(order, f.person)
		}
		byPerson[f.person] = append(byPerson[f.person], i)
	}
	var groups []*petGroup
	add := func(idx []int) {
		g := &petGroup{photos: map[string]bool{}, alive: true, sum: make([]float64, len(faces[idx[0]].emb))}
		for _, i := range idx {
			g.faces = append(g.faces, i)
			g.photos[faces[i].hash] = true
			for d, v := range faces[i].emb {
				if d < len(g.sum) {
					g.sum[d] += float64(v)
				}
			}
			if faces[i].named {
				g.named = faces[i].person
			}
		}
		groups = append(groups, g)
	}
	for _, person := range order {
		idx := byPerson[person]
		if faces[idx[0]].named {
			add(idx)
			continue
		}
		kept, alone := typicalPerPhoto(faces, idx)
		add(kept)
		for _, i := range alone {
			add([]int{i})
		}
	}
	if len(groups) > 1 {
		mergeGroups(groups, least)
	}
	var out [][]int
	for _, g := range groups {
		if g.alive {
			sort.Ints(g.faces)
			out = append(out, g.faces)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a][0] < out[b][0] })
	return out
}

// typicalPerPhoto is idx, one unnamed pet's faces, with one face per
// photo - in a photo where it has more than one, the most like all of its
// faces - and the others.
func typicalPerPhoto(faces []groupFace, idx []int) (kept, others []int) {
	perPhoto := map[string]int{}
	for _, i := range idx {
		perPhoto[faces[i].hash]++
	}
	twice := false
	for _, n := range perPhoto {
		if n > 1 {
			twice = true
			break
		}
	}
	if !twice {
		return idx, nil
	}
	sum := make([]float64, len(faces[idx[0]].emb))
	for _, i := range idx {
		for d, v := range faces[i].emb {
			if d < len(sum) {
				sum[d] += float64(v)
			}
		}
	}
	like := func(i int) float64 {
		var s float64
		for d, v := range faces[i].emb {
			if d < len(sum) {
				s += float64(v) * sum[d]
			}
		}
		return s
	}
	best := map[string]int{}
	for _, i := range idx {
		h := faces[i].hash
		if b, ok := best[h]; !ok || like(i) > like(b) {
			best[h] = i
		}
	}
	for _, i := range idx {
		if best[faces[i].hash] == i {
			kept = append(kept, i)
		} else {
			others = append(others, i)
		}
	}
	return kept, others
}

// mergeGroups merges groups, the closest two first, while two are least
// alike or more: how alike two pets' photos are on average is the dot
// product of their embeddings' sums over the product of their counts; two
// pets in one photo together may never be one, nor two with a name. The
// sums' dot products are kept for each pair (a merged pet's are its two
// pets' added), and each pet's closest other (best): a merge compares the
// merged pet with the rest, and looks again for the closest other of those
// that were closest to either of the two.
func mergeGroups(groups []*petGroup, least float32) {
	n := len(groups)
	dot := make([]float32, n*n)
	never := make([]bool, n*n)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			var s float64
			for d, v := range groups[i].sum {
				if d < len(groups[j].sum) {
					s += v * groups[j].sum[d]
				}
			}
			dot[i*n+j], dot[j*n+i] = float32(s), float32(s)
			if !mayBeOne(groups[i], groups[j]) {
				never[i*n+j], never[j*n+i] = true, true
			}
		}
	}
	size := make([]float32, n)
	for i, g := range groups {
		size[i] = float32(len(g.faces))
	}
	best := make([]int, n)
	bestLink := make([]float32, n)
	closest := func(i int) {
		best[i], bestLink[i] = -1, -2
		for j, g := range groups {
			if j == i || !g.alive || never[i*n+j] {
				continue
			}
			if l := dot[i*n+j] / (size[i] * size[j]); l > bestLink[i] {
				best[i], bestLink[i] = j, l
			}
		}
	}
	for i := range groups {
		closest(i)
	}
	for {
		a := -1
		for i, g := range groups {
			if g.alive && best[i] >= 0 && (a < 0 || bestLink[i] > bestLink[a]) {
				a = i
			}
		}
		if a < 0 || bestLink[a] < least {
			return
		}
		b := best[a]
		ga, gb := groups[a], groups[b]
		ga.faces = append(ga.faces, gb.faces...)
		if gb.named != "" {
			ga.named = gb.named
		}
		size[a] += size[b]
		gb.alive, gb.faces = false, nil
		for x := 0; x < n; x++ {
			dot[a*n+x] += dot[b*n+x]
			dot[x*n+a] = dot[a*n+x]
			never[a*n+x] = never[a*n+x] || never[b*n+x]
			never[x*n+a] = never[a*n+x]
		}
		closest(a)
		for i, g := range groups {
			if i == a || !g.alive {
				continue
			}
			if best[i] == a || best[i] == b {
				closest(i)
			} else if !never[i*n+a] {
				if l := dot[i*n+a] / (size[i] * size[a]); l > bestLink[i] {
					best[i], bestLink[i] = a, l
				}
			}
		}
	}
}

// mayBeOne is whether two pets may be one at all: not when they are in one
// photo together, nor when both have a name.
func mayBeOne(a, b *petGroup) bool {
	if a.named != "" && b.named != "" {
		return false
	}
	small, large := a.photos, b.photos
	if len(small) > len(large) {
		small, large = large, small
	}
	for h := range small {
		if large[h] {
			return false
		}
	}
	return true
}

// regroupPets is regroupPetsLocked under faceRefsMu.
func (mg *Manager) regroupPets(ses *session.Session) {
	mg.faceRefsMu.Lock()
	defer mg.faceRefsMu.Unlock()
	mg.regroupPetsLocked(ses)
}

// regroupPetsLocked groups the pets of each kind again (see above) and
// files their faces as grouped: a pet whose faces all joined another is
// merged into it, as People's merge does; faces that left their pet (one
// twice in a photo) are given to the pet they joined, or a new one. The
// caller holds faceRefsMu.
func (mg *Manager) regroupPetsLocked(ses *session.Session) {
	if ses == nil {
		return
	}
	stored, err := mg.dao.ListPetFaces()
	if err != nil {
		if !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("pets: could not list the pets to group:", err)
		}
		return
	}
	mg.pets.sinceGrouped.Store(0)
	byKind := map[int][]groupFace{}
	for _, f := range stored {
		plain, err := ses.Decrypt(f.Embedding)
		if err != nil {
			continue
		}
		byKind[f.Kind] = append(byKind[f.Kind], groupFace{id: f.ID, person: f.PersonID, hash: f.Hash, named: f.Named, emb: facerecognition.DecodeEmbedding(plain)})
	}
	// Each pet's faces, of every kind: a pet is merged into another only
	// with all of them.
	total := map[string]int{}
	for _, f := range stored {
		total[f.PersonID]++
	}
	changed := map[string]bool{}
	kinds := make([]int, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Ints(kinds)
	for _, kind := range kinds {
		faces := byKind[kind]
		pets := map[string]bool{}
		for _, f := range faces {
			pets[f.person] = true
		}
		if len(pets) > cPetGroupMax {
			log.Info("pets:", len(pets), "pets of a kind, too many to group: left as filed")
			continue
		}
		for id := range mg.fileGroupsLocked(faces, groupPets(faces, petGroupAt(kind)), total) {
			changed[id] = true
		}
	}
	if len(changed) == 0 {
		return
	}
	// Matched against again from what is stored; each pet changed chooses
	// its face again from it.
	mg.petRefs = nil
	refs, err := mg.loadPetRefsLocked(ses)
	if err != nil {
		log.Error("pets: could not list the pets just grouped:", err)
		return
	}
	for id := range changed {
		for _, kr := range refs {
			if p := kr[id]; p != nil {
				mg.chooseCoverLocked(ses, id, p)
			}
		}
	}
	mg.memories.changed()
	log.Info("pets: grouped again,", len(changed), "pet(s) changed")
}

// groupStep makes one group of grouped faces one pet: target (a new pet
// when ""), with the pets all of whose faces are there merged into it and
// the faces there of pets that are mostly elsewhere given to it.
type groupStep struct {
	target string
	merge  []string
	give   []string
}

// planGroups is how faces, grouped as groups says (groupPets' answer), are
// filed, a step for each group that changes. A group's pet is the one with
// a name in it, or else the one with most of its faces among those whose
// faces are mostly there (ties: the first in faces), or else a new one. A
// pet is merged into it only when all of its pet faces are there - total
// counts them, of every kind (nil: those in faces).
func planGroups(faces []groupFace, groups [][]int, total map[string]int) []groupStep {
	if total == nil {
		total = map[string]int{}
		for _, f := range faces {
			total[f.person]++
		}
	}
	// Where most of each pet's faces are (ties: the first group).
	home := map[string]int{}
	homeCount := map[string]int{}
	for g, idx := range groups {
		count := map[string]int{}
		for _, i := range idx {
			count[faces[i].person]++
		}
		for p, n := range count {
			if n > homeCount[p] {
				home[p], homeCount[p] = g, n
			}
		}
	}
	var steps []groupStep
	for g, idx := range groups {
		count := map[string]int{}
		target := ""
		for _, i := range idx {
			f := faces[i]
			count[f.person]++
			if f.named {
				target = f.person
			}
		}
		if target == "" {
			most := 0
			for _, i := range idx {
				p := faces[i].person
				if home[p] == g && count[p] > most {
					target, most = p, count[p]
				}
			}
		}
		var st groupStep
		st.target = target
		merged := map[string]bool{}
		for _, i := range idx {
			p := faces[i].person
			if p == target || merged[p] {
				continue
			}
			if count[p] == total[p] {
				merged[p] = true
				st.merge = append(st.merge, p)
			}
		}
		for _, i := range idx {
			if f := faces[i]; f.person != target && !merged[f.person] {
				st.give = append(st.give, f.id)
			}
		}
		if len(st.merge) > 0 || len(st.give) > 0 {
			steps = append(steps, st)
		}
	}
	return steps
}

// fileGroupsLocked files faces as groups says (planGroups), and says
// which pets changed: those merged into another are gone, the others
// chose their face again. A step whose pets changed meanwhile - a pet to
// merge named or gone, its pet gone - is left: the next grouping sees
// them as they are.
func (mg *Manager) fileGroupsLocked(faces []groupFace, groups [][]int, total map[string]int) map[string]bool {
	owner := map[string]string{}
	for _, f := range faces {
		owner[f.id] = f.person
	}
	changed := map[string]bool{}
	for _, st := range planGroups(faces, groups, total) {
		ids := append([]string(nil), st.merge...)
		if st.target != "" {
			ids = append(ids, st.target)
		}
		now, err := mg.dao.PeopleNamed(ids)
		if err != nil {
			log.Error("pets: could not check the pets to group:", err)
			return changed
		}
		held := true
		for _, id := range st.merge {
			if named, ok := now[id]; !ok || named {
				held = false
			}
		}
		if _, ok := now[st.target]; st.target != "" && !ok {
			held = false
		}
		if !held {
			continue
		}
		target := st.target
		if target == "" {
			target = uuid.New().String()
			if err := mg.dao.CreatePerson(target); err != nil {
				log.Error("pets: could not create a pet while grouping:", err)
				continue
			}
		}
		if len(st.merge) > 0 {
			if err := mg.dao.MergePeople(target, st.merge); err != nil {
				log.Error("pets: could not merge pets that are one:", err)
				continue
			}
		}
		if len(st.give) > 0 {
			if err := mg.dao.GivePetFaces(st.give, target); err != nil {
				log.Error("pets: could not give a pet's faces to the pet they are:", err)
				continue
			}
			for _, id := range st.give {
				changed[owner[id]] = true
			}
		}
		changed[target] = true
	}
	return changed
}
