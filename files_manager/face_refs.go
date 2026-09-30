// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	facerecognition "github.com/alonsovidales/otc/face_recognition"
)

// cMaxFaceRefsPerPerson caps how many of a person's faces are kept as
// matching "references" (issue #173). processFaces used to decrypt every
// stored face of the whole library for every photo and recompute each
// person's medoid over all of their faces (O(k^2)) after every single
// face, which made a full-library Reprocess take hours. Twenty faces
// chosen for variety (see personFaceRefs.add) cover a person's usual
// angles/lighting/ages well enough for nearest-neighbor matching, and
// keep the medoid at <=400 comparisons. Every face row is still stored -
// face rows are what link photos to people for search - only the
// in-memory matching set is bounded.
const cMaxFaceRefsPerPerson = 20

// faceRef is one reference face: the id of its (still stored) face row
// and its decrypted embedding.
type faceRef struct {
	id  string
	emb []float32
}

// personFaceRefs is one person's reference set plus the pairwise cosine
// similarities between its members, kept alongside so choosing which
// reference to drop (see add) costs no new cosine computations - only
// the new face's similarities to the existing references.
type personFaceRefs struct {
	refs []faceRef
	// sim[i][j] is CosineSimilarity(refs[i].emb, refs[j].emb); the
	// diagonal is unused.
	sim [][]float32
}

// faceRefs is the whole library's matching set, person id -> references.
// Built once per process from the database (see Manager.loadFaceRefs) and
// then kept up to date incrementally by processFaces.
type faceRefs map[string]*personFaceRefs

// add offers a face to this person's references and reports whether it
// was kept. Below cMaxFaceRefsPerPerson it's always kept. At the cap:
//   - an outlier (average similarity to the current references below the
//     same-person threshold) is not kept - a misclustered or poor-quality
//     face shouldn't displace a good one or pull future matches toward
//     it. Its face row is still stored by the caller either way;
//   - otherwise it's kept and the most redundant reference is dropped:
//     the one whose highest similarity to any other reference is the
//     largest, i.e. the one closest to a near-duplicate. That keeps the
//     set diverse (profiles, lighting, ages) instead of collapsing into
//     near-identical frontal shots, which would match the person's other
//     looks worse. The new face itself can be the one dropped, when it's
//     the most redundant.
func (p *personFaceRefs) add(id string, emb []float32) bool {
	sims := make([]float32, len(p.refs))
	var total float32
	for i, r := range p.refs {
		sims[i] = facerecognition.CosineSimilarity(emb, r.emb)
		total += sims[i]
	}
	if len(p.refs) >= cMaxFaceRefsPerPerson && !facerecognition.IsSamePersonScore(total/float32(len(p.refs))) {
		return false
	}

	for i := range p.sim {
		p.sim[i] = append(p.sim[i], sims[i])
	}
	p.sim = append(p.sim, append(sims, 0))
	p.refs = append(p.refs, faceRef{id: id, emb: emb})

	if len(p.refs) > cMaxFaceRefsPerPerson {
		p.remove(p.mostRedundant())
	}
	return true
}

// mostRedundant returns the index of the reference whose max similarity
// to any other reference is highest. Ties go to the earliest (oldest)
// one, so the result is deterministic.
func (p *personFaceRefs) mostRedundant() int {
	worst, worstScore := 0, float32(-2) // cosine is never below -1
	for i := range p.refs {
		best := float32(-2)
		for j := range p.refs {
			if i != j && p.sim[i][j] > best {
				best = p.sim[i][j]
			}
		}
		if best > worstScore {
			worst, worstScore = i, best
		}
	}
	return worst
}

func (p *personFaceRefs) remove(idx int) {
	p.refs = append(p.refs[:idx], p.refs[idx+1:]...)
	p.sim = append(p.sim[:idx], p.sim[idx+1:]...)
	for i := range p.sim {
		p.sim[i] = append(p.sim[i][:idx], p.sim[i][idx+1:]...)
	}
}

// identified returns the references in face_recognition's own type, for
// MedoidAndCohesion.
func (p *personFaceRefs) identified() []facerecognition.IdentifiedEmbedding {
	out := make([]facerecognition.IdentifiedEmbedding, len(p.refs))
	for i, r := range p.refs {
		out[i] = facerecognition.IdentifiedEmbedding{ID: r.id, Embedding: r.emb}
	}
	return out
}

// add offers a face to personID's references, creating the person's set
// on first use - see personFaceRefs.add for the selection rule.
func (fr faceRefs) add(personID, faceID string, emb []float32) bool {
	p := fr[personID]
	if p == nil {
		p = &personFaceRefs{}
		fr[personID] = p
	}
	return p.add(faceID, emb)
}
