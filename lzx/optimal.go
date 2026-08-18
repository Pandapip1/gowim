package lzx

import "sort"

// findMatchesOptimal runs a forward shortest-path DP over the whole chunk,
// using the same binary-tree match finder as findMatches (see matcher.go),
// as an attempt at a genuinely more "optimal" parse than the bounded 3-way
// lookahead there.
//
// # Honest scope: a bounded multi-state beam, not wimlib's full parser
//
// A fully faithful optimal parse with LZX's repeat-offset queue needs to
// explore the combinatorics of every distinct queue *state* (which of the
// three most-recently-used offsets is which) reachable at every position,
// since a higher-cost path to some position might carry a queue state that
// enables much cheaper matches later -- e.g. wimlib's
// lzx_compress_near_optimal tracks multiple live queue-state hypotheses
// per position for exactly this reason.
//
// This function now does the same in spirit, but bounded: at each position
// it keeps up to beamWidth distinct (cost, queue-state) hypotheses (a beam
// search over queue states, not just the single cheapest arrival). A
// higher-cost path that carries a different, more useful queue state is no
// longer thrown away outright -- it survives as long as it's one of the
// beamWidth cheapest distinct-queue hypotheses at that position. This is
// still not wimlib's approach: wimlib's near-optimal parser is not beam
// limited in the same way and additionally considers a much larger edge
// set per position (near-exhaustive length/offset exploration plus
// multiple forward passes). What follows is a genuine, bounded
// approximation of the same idea, not a full reimplementation.
//
// # Bounded edge and state counts
//
// Both the number of queue-state hypotheses kept per position (beamWidth)
// and the number of edges considered per hypothesis (see
// repeatLengthSamples and maxFreshCandidates below) are bounded, to keep
// worst-case cost from blowing up on pathological (e.g. highly
// repetitive) input. Real-world measurements of this bounding's actual
// cost/benefit are in gowim's own TODO.md.
func findMatchesOptimal(data []byte, model costModel) []token {
	n := len(data)
	if n == 0 {
		return nil
	}

	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	left := make([]int32, n)
	right := make([]int32, n)

	hash := func(i int) uint32 {
		v := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16
		return (v * 2654435761) >> (32 - hashBits)
	}

	matchLenCapped := func(c, pos int) (l, limit int) {
		limit = n - pos
		if n-c < limit {
			limit = n - c
		}
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		for l < limit && data[c+l] == data[pos+l] {
			l++
		}
		return l, limit
	}

	// freshCandidate is one Pareto-frontier (length, offset) pair found by
	// bstSearchAll below: within the frontier, a strictly longer candidate
	// always has a strictly larger (more expensive) offset, so a shorter
	// candidate is only kept if no longer-or-equal candidate has an
	// equal-or-cheaper offset.
	type freshCandidate struct {
		length int
		offset int
	}

	// maxFreshCandidates bounds how many distinct fresh-offset candidates
	// the DP considers per position (beyond this, only the longest
	// candidates are kept -- see bstSearchAll), keeping worst-case DP edge
	// count bounded regardless of how many non-dominated candidates a
	// pathological input could otherwise produce.
	const maxFreshCandidates = 8

	// bstSearchAll walks the BST rooted at head[hash(pos)] exactly as
	// bstSearch does (matcher.go), but instead of keeping only the single
	// longest match, collects every strictly-longer-than-previous
	// candidate seen along the walk (a Pareto frontier by construction,
	// since the walk only records a candidate when it beats the best
	// length seen so far), capped at maxFreshCandidates (keeping the
	// longest ones if more are found).
	bstSearchAll := func(pos int) []freshCandidate {
		if pos+3 > n {
			return nil
		}
		var cands []freshCandidate
		bestLen := 0
		cur := head[hash(pos)]
		depth := 0
		for cur >= 0 && depth < maxChainLen {
			c := int(cur)
			l, limit := matchLenCapped(c, pos)
			if l > bestLen && l >= minMatch {
				bestLen = l
				cands = append(cands, freshCandidate{length: l, offset: pos - c})
				if len(cands) > maxFreshCandidates {
					cands = cands[1:] // drop the shortest kept so far
				}
			}
			if l >= limit || data[c+l] < data[pos+l] {
				cur = right[c]
			} else {
				cur = left[c]
			}
			depth++
		}
		return cands
	}

	bstInsert := func(pos int) {
		if pos+3 > n {
			return
		}
		h := hash(pos)
		cur := head[h]
		ptrLo := &left[pos]
		ptrHi := &right[pos]
		depth := 0
		for cur >= 0 && depth < maxChainLen {
			c := int(cur)
			l, limit := matchLenCapped(c, pos)
			if l >= limit || data[c+l] < data[pos+l] {
				*ptrLo = int32(c)
				ptrLo = &right[c]
				cur = right[c]
			} else {
				*ptrHi = int32(c)
				ptrHi = &left[c]
				cur = left[c]
			}
			depth++
		}
		*ptrLo = -1
		*ptrHi = -1
		head[h] = int32(pos)
	}

	// beamWidth bounds how many distinct queue-state hypotheses survive at
	// each position. A higher-cost arrival is kept only if it carries a
	// queue state not already covered by a cheaper arrival, and only if
	// it's among the beamWidth cheapest such distinct-queue arrivals.
	const beamWidth = 4

	// dpEdge records how one state at one position was reached: either a
	// literal (isMatch false) or a match, plus the predecessor position
	// and which of that predecessor's states it came from.
	type dpEdge struct {
		isMatch   bool
		literal   byte
		offset    int
		length    int
		repeat    int
		from      int
		fromState int
	}

	type dpState struct {
		cost  int
		queue [numRecentOffsets]int32
		edge  dpEdge
	}

	states := make([][]dpState, n+1)
	states[0] = []dpState{{
		cost:  0,
		queue: [numRecentOffsets]int32{1, 1, 1},
		edge:  dpEdge{from: -1, fromState: -1},
	}}

	// mergeState folds a newly-relaxed (cost, queue) arrival into
	// states[to]: if a hypothesis with the same queue state already
	// exists there, it's replaced only if the new arrival is cheaper;
	// otherwise the new hypothesis is appended as a distinct candidate.
	// Bounding to beamWidth happens later, in pruneState, once all
	// arrivals into a position have been folded in.
	mergeState := func(to int, newCost int, newQueue [numRecentOffsets]int32, edge dpEdge) {
		for idx := range states[to] {
			if states[to][idx].queue == newQueue {
				if newCost < states[to][idx].cost {
					states[to][idx].cost = newCost
					states[to][idx].edge = edge
				}
				return
			}
		}
		states[to] = append(states[to], dpState{cost: newCost, queue: newQueue, edge: edge})
	}

	// pruneState trims states[pos] down to the beamWidth cheapest distinct
	// queue-state hypotheses. It's called on position i right before i is
	// used as a source of outgoing edges, by which point every edge that
	// could ever land on i (all of which come from positions < i) has
	// already been folded in via mergeState.
	pruneState := func(pos int) {
		s := states[pos]
		if len(s) <= beamWidth {
			return
		}
		sort.Slice(s, func(a, b int) bool { return s[a].cost < s[b].cost })
		states[pos] = append([]dpState(nil), s[:beamWidth]...)
	}

	literalCost := func(b byte) int {
		if model.mainLens != nil {
			if c := int(model.mainLens[b]); c > 0 {
				return c
			}
		}
		return flatLiteralBits
	}

	// repeatLengthSamples bounds how many distinct lengths are tried per
	// repeat-offset candidate: the full length found, plus a midpoint
	// shorter length (letting the DP "under-shoot" a long repeat run if a
	// different choice afterward is cheaper), rather than every length
	// from minMatchLen up to the full length -- trying every length is
	// what a from-scratch optimal parser would do, but is unbounded cost
	// for highly repetitive input (a run of identical bytes can have a
	// repeat length up to maxMatchLen at nearly every position).
	repeatLengthSamples := func(full int) []int {
		if full <= minMatchLen+4 {
			return []int{full}
		}
		return []int{full, full / 2}
	}

	for i := 0; i < n; i++ {
		pruneState(i)
		cur := states[i]
		if len(cur) == 0 {
			// Unreachable: cannot happen in practice since every position
			// is reachable via a chain of literal edges from 0, but guard
			// defensively rather than index an empty slice below.
			bstInsert(i)
			continue
		}

		freshCands := bstSearchAll(i)
		litCost := literalCost(data[i])

		for si, s := range cur {
			mergeState(i+1, s.cost+litCost, s.queue, dpEdge{literal: data[i], repeat: -1, from: i, fromState: si})

			for k := 0; k < numRecentOffsets; k++ {
				off := s.queue[k]
				j := i - int(off)
				if j < 0 {
					continue
				}
				l, _ := matchLenCapped(j, i)
				if l < minMatchLen {
					continue
				}
				for _, length := range repeatLengthSamples(l) {
					if length < minMatchLen {
						continue
					}
					c := model.matchCost(k, length)
					nq := applyMatch(s.queue, k, int(off))
					mergeState(i+length, s.cost+c, nq, dpEdge{isMatch: true, offset: int(off), length: length, repeat: k, from: i, fromState: si})
				}
			}

			for _, cand := range freshCands {
				slot := offsetSlot(uint32(cand.offset))
				extraBits := int(lzxExtraOffsetBits[slot])
				c := model.matchCost(slot, cand.length) + extraBits
				nq := applyMatch(s.queue, -1, cand.offset)
				mergeState(i+cand.length, s.cost+c, nq, dpEdge{isMatch: true, offset: cand.offset, length: cand.length, repeat: -1, from: i, fromState: si})
			}
		}

		bstInsert(i)
	}

	finalStates := states[n]
	bestIdx := 0
	for idx := 1; idx < len(finalStates); idx++ {
		if finalStates[idx].cost < finalStates[bestIdx].cost {
			bestIdx = idx
		}
	}

	var toks []token
	pos, si := n, bestIdx
	for pos > 0 {
		e := states[pos][si].edge
		if e.isMatch {
			toks = append(toks, token{isMatch: true, offset: e.offset, length: e.length, repeat: e.repeat})
		} else {
			toks = append(toks, token{literal: e.literal, repeat: -1})
		}
		pos, si = e.from, e.fromState
	}
	for l, r := 0, len(toks)-1; l < r; l, r = l+1, r-1 {
		toks[l], toks[r] = toks[r], toks[l]
	}
	return toks
}
