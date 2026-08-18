package lzx

// findMatchesOptimal runs a forward shortest-path DP over the whole chunk,
// using the same binary-tree match finder as findMatches (see matcher.go),
// as an attempt at a genuinely more "optimal" parse than the bounded 3-way
// lookahead there.
//
// # Honest scope: this is NOT wimlib's full near-optimal parser
//
// A fully faithful optimal parse with LZX's repeat-offset queue needs to
// explore the combinatorics of every distinct queue *state* (which of the
// three most-recently-used offsets is which) reachable at every position,
// since a higher-cost path to some position might carry a queue state that
// enables much cheaper matches later -- e.g. wimlib's
// lzx_compress_near_optimal tracks multiple live queue-state hypotheses
// per position for exactly this reason. That is a substantially larger
// undertaking (and a much larger surface for subtle bugs) than this
// package has attempted elsewhere.
//
// This function instead tracks a SINGLE queue trajectory per position: the
// queue state associated with whichever predecessor gives the minimum
// cost to reach that position. This is a real approximation, not a
// footnote -- a slightly costlier path to position i might in principle
// carry a queue state that enables a much cheaper match from i onward,
// and this DP would never discover that trade-off, since it only ever
// remembers the single cheapest arrival at each position. What this DP
// does still do that findMatches' bounded lookahead does not: explore
// every match length (not just the longest) for repeat-offset candidates,
// and choose the true shortest path across the WHOLE chunk under that
// single-trajectory model, rather than only ever looking 1-2 positions
// ahead.
//
// # Bounded edge counts
//
// Trying every possible (position, length) pair without bounds is what
// makes real optimal parsers expensive; this function bounds the edges
// considered per position to keep worst-case cost roughly comparable to
// the other passes in this package (see repeatLengthSamples and
// maxFreshCandidates below), rather than the unbounded "try every length
// at every candidate offset" a from-scratch optimal parser would use.
// Real-world measurements of this bounding's actual cost/benefit are in
// gowim's own TODO.md.
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

	const inf = 1 << 30

	// dpEdge records how the shortest path reached one position: either a
	// literal (isMatch false) or a match, plus the predecessor position.
	type dpEdge struct {
		isMatch bool
		literal byte
		offset  int
		length  int
		repeat  int
		from    int
	}

	cost := make([]int, n+1)
	for i := range cost {
		cost[i] = inf
	}
	cost[0] = 0
	queueAt := make([][numRecentOffsets]int32, n+1)
	queueAt[0] = [numRecentOffsets]int32{1, 1, 1}
	edgeInto := make([]dpEdge, n+1)

	relax := func(to, newCost int, e dpEdge, newQueue [numRecentOffsets]int32) {
		if newCost < cost[to] {
			cost[to] = newCost
			edgeInto[to] = e
			queueAt[to] = newQueue
		}
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
		if cost[i] >= inf {
			// Unreachable: cannot happen in practice since every position
			// is reachable via a chain of literal edges from 0, but guard
			// defensively against ever indexing queueAt[i] uninitialized.
			bstInsert(i)
			continue
		}
		q := queueAt[i]

		relax(i+1, cost[i]+literalCost(data[i]), dpEdge{literal: data[i], repeat: -1, from: i}, q)

		for k := 0; k < numRecentOffsets; k++ {
			off := q[k]
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
				nq := applyMatch(q, k, int(off))
				relax(i+length, cost[i]+c, dpEdge{isMatch: true, offset: int(off), length: length, repeat: k, from: i}, nq)
			}
		}

		for _, cand := range bstSearchAll(i) {
			slot := offsetSlot(uint32(cand.offset))
			extraBits := int(lzxExtraOffsetBits[slot])
			c := model.matchCost(slot, cand.length) + extraBits
			nq := applyMatch(q, -1, cand.offset)
			relax(i+cand.length, cost[i]+c, dpEdge{isMatch: true, offset: cand.offset, length: cand.length, repeat: -1, from: i}, nq)
		}

		bstInsert(i)
	}

	var toks []token
	for pos := n; pos > 0; {
		e := edgeInto[pos]
		if e.isMatch {
			toks = append(toks, token{isMatch: true, offset: e.offset, length: e.length, repeat: e.repeat})
		} else {
			toks = append(toks, token{literal: e.literal, repeat: -1})
		}
		pos = e.from
	}
	for l, r := 0, len(toks)-1; l < r; l, r = l+1, r-1 {
		toks[l], toks[r] = toks[r], toks[l]
	}
	return toks
}
