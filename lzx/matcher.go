package lzx

// token is one literal or match emitted by the match finder.
type token struct {
	isMatch bool
	literal byte
	offset  int // match distance in bytes (>= 1)
	length  int // match length in bytes ([minMatchLen, maxMatchLen])
	// repeat is the repeat-offset LRU queue slot (0, 1, or 2) this match
	// reuses, or -1 if it uses a fresh, non-recent offset. See the
	// package-level doc for how the queue is tracked and why this matters.
	repeat int
}

const (
	hashBits    = 16
	hashSize    = 1 << hashBits
	minMatch    = 3 // minimum length for a *fresh*-offset match (see below)
	maxChainLen = 96

	// Flat per-symbol bit-cost estimates used by costModel when it has no
	// real Huffman codeword lengths yet (findMatches' first pass -- see
	// compress() in encode.go). These are rough constants, not measured
	// from this chunk's actual data: real codeword lengths for the
	// commonly-used literal/main/length symbols in typical data cluster in
	// roughly this range, so this is "good enough to rank candidates
	// sensibly" for a first pass whose whole purpose is to produce
	// *some* Huffman table for the second, real pass to refine against.
	flatLiteralBits = 8
	flatMainBits    = 8
	flatLenBits     = 8
)

// costModel estimates the bit cost of literals and matches, used to choose
// among candidate matches (including whether a repeat-offset match is
// worth preferring over a longer fresh-offset one) and to compare a
// lazy-matching peek against the current position's best candidate. A zero
// costModel (nil Lens slices) uses the flat estimates above -- this is
// findMatches' first pass. A costModel built from a first pass's actual
// Huffman codeword lengths is used for a refining second pass (see
// compress() in encode.go): this is a standard two-pass technique (parse,
// build a code, re-parse against the real code's costs) for approximating
// the joint parse/code optimization a full iterative optimal parser would
// do, without that full complexity.
//
// Literal cost is always the flat estimate, even in the second pass:
// unlike match costs (which vary hugely with offset slot, 0-17 extra bits),
// real literal codeword lengths vary comparatively little, so refining
// match cost captures the great majority of the benefit for much less
// complexity.
type costModel struct {
	mainLens []byte // nil => flatMainBits/flatLenBits estimates
	lenLens  []byte
}

// matchCost returns the estimated bit cost of a match's main-code symbol
// (and secondary length symbol, if needed), NOT including extra offset
// bits (the caller adds those separately, since they depend only on the
// offset slot, not on any Huffman table).
func (m costModel) matchCost(slot, length int) int {
	lengthField := length - minMatchLen
	header := lengthField
	if header > numPrimaryLens {
		header = numPrimaryLens
	}
	mainBits := flatMainBits
	if m.mainLens != nil {
		mainSym := numChars + slot*numLenHeaders + header
		mainBits = int(m.mainLens[mainSym])
	}
	if header != numPrimaryLens {
		return mainBits
	}
	lenBits := flatLenBits
	if m.lenLens != nil {
		lsym := lengthField - numPrimaryLens
		lenBits = int(m.lenLens[lsym])
	}
	return mainBits + lenBits
}

// matchValue estimates how many bits a match of the given slot/length/
// extraBits saves versus emitting length literals instead, using
// flatLiteralBits per literal. Higher is better; a value <= 0 means the
// match is not worth using at all.
func (m costModel) matchValue(slot, length, extraBits int) int {
	return length*flatLiteralBits - m.matchCost(slot, length) - extraBits
}

// candidateMatch is the single best match (repeat-offset or fresh) found at
// one position, as chosen by chooseMatch below.
type candidateMatch struct {
	found  bool
	offset int
	length int
	repeat int // -1 if fresh
	value  int // costModel.matchValue of this candidate; only valid if found
}

// findMatches runs a bounded 3-way lookahead LZ77 parse over data using a
// binary-tree match finder for fresh offsets, plus a direct check of the
// LZX repeat-offset LRU queue (the three most recently used match offsets)
// at every position. Per this package's documented encoder scope (see
// lzx.go and compress() in encode.go), this is a fixed depth-2 lookahead
// (see the "Bounded 3-way lookahead" comment below), not a full optimal/DP
// parse over the whole chunk: at each position it evaluates the best
// repeat-offset candidate, the best fresh-offset candidate, and "emit a
// literal instead", each combined with a single non-recursive 1-step
// continuation value, and commits whichever totals highest. Candidate
// values come from costModel (see above), which replaces a simpler earlier
// version of this package that used a flat "repeat must be within N bytes
// of fresh" bonus instead of an actual, if approximate, bit-cost
// comparison. This generalizes the classic one-step "lazy matching"
// technique (as used by, e.g., zlib's deflate and wimlib's own
// non-near-optimal compression levels) from "compare a single pre-picked
// best candidate against literal-then-next-best" to "compare each kind of
// candidate's own continuation independently" -- still a fixed, bounded
// lookahead, not an iterative whole-chunk bit-cost model.
//
// The repeat-offset queue is tracked exactly as the decoder maintains it
// (see decode.go's recentOffsets handling, itself ported from wimlib's
// lzx_decompress.c): starts at {1, 1, 1}, and after every match, the used
// offset is swapped into slot 0 (a no-op if it was already slot 0), while a
// fresh offset shifts the whole queue. This must stay in lockstep with the
// decoder's transitions since encode.go emits the resulting repeat slot
// (0, 1, or 2) directly as the match's offset-slot symbol, with no explicit
// offset bits.
//
// A match's length is always capped at maxMatchLen (257) by matchLenAt, so
// there is no separate "split a long match" step: a run longer than
// maxMatchLen is simply rediscovered on the next iteration, at which point
// it is a repeat-offset match (queue slot 0), which is exactly as compact.
func findMatches(data []byte, model costModel) []token {
	n := len(data)
	var toks []token
	if n == 0 {
		return toks
	}

	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	// left/right are per-position child links for a binary search tree
	// rooted at head[hash(pos)], ordered lexicographically by each
	// position's suffix -- see bstSearch/bstInsert below. This replaces an
	// earlier version of this package that used a simple hash-chain
	// (recency-ordered linked list) here: a BST gives meaningfully better
	// candidates within the same bounded comparison budget (maxChainLen),
	// since it can discard whole subtrees known to be lexicographically on
	// the wrong side rather than walking a flat recency list, the same
	// technique used by real encoders' "bt" match finders (e.g. the LZMA
	// SDK's bt4). See gowim's own TODO.md for the real, measured
	// improvement this made.
	left := make([]int32, n)
	right := make([]int32, n)
	inserted := make([]bool, n)

	hash := func(i int) uint32 {
		v := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16
		return (v * 2654435761) >> (32 - hashBits)
	}

	// matchLenCapped returns the common-prefix length between data[c:] and
	// data[pos:], and the length limit it was capped against (buffer end
	// or maxMatchLen) -- callers use l >= limit to know it's safe to stop
	// without reading data[c+l]/data[pos+l] (which may be out of bounds).
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

	// bstSearch finds the best match for the suffix starting at pos among
	// all previously-inserted positions, without inserting pos itself, so
	// it is safe to call speculatively for a lazy-matching peek (see
	// chooseMatch below). It walks the BST rooted at head[hash(pos)],
	// following the same left/right comparison logic bstInsert uses to
	// place new nodes, bounded to maxChainLen comparisons.
	bstSearch := func(pos int) (bestLen, bestOff int) {
		if pos+3 > n {
			return 0, 0
		}
		cur := head[hash(pos)]
		depth := 0
		for cur >= 0 && depth < maxChainLen {
			c := int(cur)
			l, limit := matchLenCapped(c, pos)
			if l > bestLen {
				bestLen = l
				bestOff = pos - c
			}
			if l >= limit || data[c+l] < data[pos+l] {
				cur = right[c]
			} else {
				cur = left[c]
			}
			depth++
		}
		return bestLen, bestOff
	}

	// bstInsert links position pos into the BST rooted at head[hash(pos)],
	// per the standard "insert while descending" binary-tree match-finder
	// algorithm: pos becomes the new root for its hash bucket, with the
	// old tree split into pos's left/right subtrees by lexicographic
	// comparison against pos's own suffix as we descend (bounded to
	// maxChainLen comparisons, same budget as bstSearch, so very deep
	// buckets are simply cut off rather than fully rebalanced). Idempotent
	// (guarded by inserted[]) since a position may be visited once by the
	// lazy peek's read-only bstSearch and later actually committed by the
	// main loop.
	bstInsert := func(pos int) {
		if pos+3 > n || inserted[pos] {
			return
		}
		inserted[pos] = true

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

	matchLenAt := func(i, j int) int {
		limit := n - i
		if n-j < limit {
			limit = n - j
		}
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		l := 0
		for l < limit && data[i+l] == data[j+l] {
			l++
		}
		return l
	}

	repeatLenAt := func(i int, off int32) int {
		j := i - int(off)
		if j < 0 {
			return 0
		}
		return matchLenAt(j, i)
	}

	// queue mirrors the decoder's recentOffsets, starting at {1, 1, 1} (see
	// decode.go). A repeat-offset match at position i and queue value off
	// references data[i-off:], which is only valid when off <= i.
	var queue [numRecentOffsets]int32 = [numRecentOffsets]int32{1, 1, 1}

	// bestRepeatCandidate and bestFreshCandidate each find the single best
	// candidate of their kind at position i given queue, without mutating
	// any state (hash table, tree, or queue), so both are safe to call
	// speculatively for the bounded lookahead below.
	bestRepeatCandidate := func(i int, queue [numRecentOffsets]int32) candidateMatch {
		var best candidateMatch
		for k := 0; k < numRecentOffsets; k++ {
			l := repeatLenAt(i, queue[k])
			if l < minMatchLen {
				continue
			}
			v := model.matchValue(k, l, 0)
			if !best.found || v > best.value {
				best = candidateMatch{found: true, offset: int(queue[k]), length: l, repeat: k, value: v}
			}
		}
		return best
	}

	bestFreshCandidate := func(i int) candidateMatch {
		bestLen, bestOff := bstSearch(i)
		if bestLen < minMatch {
			return candidateMatch{}
		}
		slot := offsetSlot(uint32(bestOff))
		extraBits := int(lzxExtraOffsetBits[slot])
		return candidateMatch{found: true, offset: bestOff, length: bestLen, repeat: -1, value: model.matchValue(slot, bestLen, extraBits)}
	}

	// chooseMatch finds the single best-value candidate (repeat or fresh)
	// at position i, or the zero value if neither is worth using over a
	// literal.
	chooseMatch := func(i int, queue [numRecentOffsets]int32) candidateMatch {
		best := bestRepeatCandidate(i, queue)
		if fresh := bestFreshCandidate(i); fresh.found && (!best.found || fresh.value > best.value) {
			best = fresh
		}
		if best.found && best.value <= 0 {
			return candidateMatch{}
		}
		return best
	}

	// applyMatch returns the recent-offsets queue that results from
	// choosing m, without mutating q (queue is a fixed-size array, an
	// ordinary Go value type, so this returns an independent copy) --
	// used by the bounded lookahead below to evaluate a candidate's
	// continuation without committing to it.
	applyMatch := func(q [numRecentOffsets]int32, m candidateMatch) [numRecentOffsets]int32 {
		if m.repeat >= 0 {
			if m.repeat != 0 {
				used := q[m.repeat]
				q[m.repeat] = q[0]
				q[0] = used
			}
			return q
		}
		q[2] = q[1]
		q[1] = q[0]
		q[0] = int32(m.offset)
		return q
	}

	insertRange := func(start, end int) {
		for p := start; p < end; p++ {
			bstInsert(p)
		}
	}

	// continuationValue returns chooseMatch(pos, q).value, or 0 if pos is
	// past the end of data or no match is found there -- used to score a
	// candidate's 1-step continuation below.
	continuationValue := func(pos int, q [numRecentOffsets]int32) int {
		if pos >= n {
			return 0
		}
		return chooseMatch(pos, q).value
	}

	// Bounded 3-way lookahead: at each position, evaluate the best repeat
	// candidate, the best fresh candidate, and "emit a literal", each
	// combined with its own 1-step continuation value (continuationValue
	// above, itself a single non-recursive chooseMatch call, so this stays
	// a fixed depth-2 evaluation, never a full whole-chunk search) --
	// then commit whichever option has the highest 2-step total. This is
	// a deliberately bounded generalization of one-step lazy matching
	// (which only ever compared a single pre-selected best candidate
	// against "literal, then re-decide"): here, taking the repeat
	// candidate now (even if its own immediate value is lower than the
	// fresh candidate's) can be the better overall choice if it sets up a
	// much better continuation, which a single best-of-both comparison
	// would never consider. This is NOT a full optimal/DP parse over the
	// whole chunk -- a real DP would need to explore the combinatorics of
	// every possible repeat-offset-queue state reachable at every
	// position, which this package does not attempt (see gowim's own
	// TODO.md for why: the added complexity and risk was judged not
	// worthwhile given the modest, measured returns from similar steps
	// here).
	type lookaheadOption struct {
		cand  candidateMatch // zero value means "emit a literal instead"
		total int
	}

	i := 0
	for i < n {
		rep := bestRepeatCandidate(i, queue)
		fresh := bestFreshCandidate(i)
		// bestFreshCandidate (via bstSearch) must run before bstInsert(i):
		// inserting i's own tree entry first would let it match against
		// itself at offset 0.
		bstInsert(i)

		var best lookaheadOption
		have := false
		consider := func(o lookaheadOption) {
			if !have || o.total > best.total {
				best = o
				have = true
			}
		}

		consider(lookaheadOption{total: continuationValue(i+1, queue)}) // literal now
		if rep.found && rep.value > 0 {
			consider(lookaheadOption{cand: rep, total: rep.value + continuationValue(i+rep.length, applyMatch(queue, rep))})
		}
		if fresh.found && fresh.value > 0 {
			consider(lookaheadOption{cand: fresh, total: fresh.value + continuationValue(i+fresh.length, applyMatch(queue, fresh))})
		}

		if !best.cand.found {
			toks = append(toks, token{literal: data[i], repeat: -1})
			i++
			continue
		}

		m := best.cand
		toks = append(toks, token{isMatch: true, offset: m.offset, length: m.length, repeat: m.repeat})
		queue = applyMatch(queue, m)
		insertRange(i, i+m.length)
		i += m.length
	}

	return toks
}
