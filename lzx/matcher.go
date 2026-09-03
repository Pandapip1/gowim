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
	mainLens    []byte // nil => flatMainBits/flatLenBits estimates
	lenLens     []byte
	alignedLens []byte // nil => extra offset bits costed as raw bits (see offsetExtraCost)
}

// matchCost returns the estimated bit cost of a match's main-code symbol
// (and secondary length symbol, if needed), NOT including extra offset
// bits (the caller adds those separately, since they depend only on the
// offset slot, not on any Huffman table).
//
// A real codeword length of 0 means "this symbol has no codeword at all in
// the current table" (buildLengths' documented behavior for zero-frequency
// symbols -- see huffman.go), not "this symbol is free": using it, once
// this round's table becomes final, would cost whatever bits the *next*
// real rebuild assigns it, not zero. Treating a 0 as a real, usable cost
// (a bug found and fixed 2026-08-18 -- see gowim's own TODO.md) made every
// brand-new main symbol -- most consequentially, a freshly-introduced
// length-2 hash2 offset slot that had zero frequency in the previous
// round's table -- look artificially, wrongly free, causing exactly the
// kind of runaway over-selection that made native hash2 integration
// regress the real benchmark until this was caught. literalCost (below)
// already guarded against the identical situation for literal symbols;
// this function did not, for either the main or secondary length symbol.
func (m costModel) matchCost(slot, length int) int {
	lengthField := length - minMatchLen
	header := lengthField
	if header > numPrimaryLens {
		header = numPrimaryLens
	}
	mainBits := flatMainBits
	if m.mainLens != nil {
		mainSym := numChars + slot*numLenHeaders + header
		if c := int(m.mainLens[mainSym]); c > 0 {
			mainBits = c
		}
	}
	if header != numPrimaryLens {
		return mainBits
	}
	lenBits := flatLenBits
	if m.lenLens != nil {
		lsym := lengthField - numPrimaryLens
		if c := int(m.lenLens[lsym]); c > 0 {
			lenBits = c
		}
	}
	return mainBits + lenBits
}

// literalCost returns the estimated bit cost of literal byte b: its real
// Huffman codeword length if the model has one, else the flat estimate.
// Real per-byte costs in typical binary data vary far more than a flat
// guess suggests -- e.g. a common padding byte can cost under 3 bits while
// a rare byte costs upwards of 12-15 bits in the same chunk -- so using
// the real length here (once known, from a first pass) measurably changes
// whether a match is worth its cost relative to the literals it replaces.
func (m costModel) literalCost(b byte) int {
	if m.mainLens != nil {
		if c := int(m.mainLens[b]); c > 0 {
			return c
		}
	}
	return flatLiteralBits
}

// offsetExtraCost estimates the bit cost of a fresh match's extra offset
// bits at the given slot/offset: the plain raw-bit count if the model has
// no aligned-code table yet, or -- once one is known (see refineParse in
// encode.go, which rebuilds this every round from the previous round's own
// chosen tokens) -- the real Huffman cost of the low numAlignedOffsetBits
// bits via that table plus the remaining raw bits, matching what an
// ALIGNED block would actually spend on this exact offset.
//
// This mirrors wimlib's own real near-optimal parser: found by reading its
// source (src/lzx_compress.c, CONSIDER_ALIGNED_COSTS) that it folds an
// aligned-cost estimate into match costs *during* the parse itself, not
// only when choosing VERBATIM vs ALIGNED after the fact (which this
// package's encodeBlock fan-out in encode.go still also does, unchanged,
// as the final correctness-preserving measurement) -- see gowim's own
// TODO.md for the investigation that found this gap. Without this, the
// parse's own match/offset choices are blind to the possibility that an
// ALIGNED block would end up cheaper for a given offset's low bits, which
// can steer it toward a different, non-aligned-friendly offset than the
// one that would actually encode smallest.
func (m costModel) offsetExtraCost(slot, offset int) int {
	extraBits := int(lzxExtraOffsetBits[slot])
	if m.alignedLens == nil || slot < minAlignedOffsetSlot {
		return extraBits
	}
	extra := uint32(offset) - uint32(lzxOffsetSlotBase[slot])
	asym := extra & (alignedCodeNumSymbols - 1)
	// Same zero-means-unencoded-not-free fix as matchCost above: a real
	// codeword length of 0 for this aligned symbol means it has no
	// codeword yet, not that it's free, so fall back to the raw-bits cost
	// rather than reading it as 0.
	alignedBits := numAlignedOffsetBits
	if c := int(m.alignedLens[asym]); c > 0 {
		alignedBits = c
	}
	return (extraBits - numAlignedOffsetBits) + alignedBits
}

// matchValue estimates how many bits a match of the given slot/length/
// extraBits saves versus emitting its length literals instead, given
// litCost (the real summed literalCost of those specific length bytes,
// not a flat length*flatLiteralBits guess -- see literalCost above for
// why that distinction matters). Higher is better; a value <= 0 means the
// match is not worth using at all.
func (m costModel) matchValue(slot, length, extraBits, litCost int) int {
	return litCost - m.matchCost(slot, length) - extraBits
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

// applyMatch returns the recent-offsets queue that results from choosing a
// match with the given repeat/offset fields, without mutating q (queue is
// a fixed-size array, an ordinary Go value type, so this returns an
// independent copy). Shared by findMatches' bounded lookahead and
// findMatchesOptimal's DP (see optimal.go), both of which need to evaluate
// a candidate's effect on the queue without necessarily committing to it.
func applyMatch(q [numRecentOffsets]int32, repeat, offset int) [numRecentOffsets]int32 {
	if repeat >= 0 {
		if repeat != 0 {
			used := q[repeat]
			q[repeat] = q[0]
			q[0] = used
		}
		return q
	}
	q[2] = q[1]
	q[1] = q[0]
	q[0] = int32(offset)
	return q
}

// buildHash2PrevOcc precomputes, for every position p in data, the most
// recent EARLIER position with the same 2-byte value at data[p:p+2] (or -1
// if none), via a direct 65536-entry table keyed by the exact 2-byte value
// (not a lossy hash, since a 2-byte pair has exactly 65536 possible
// values). Shared by findMatches and findMatchesOptimal to make length-2
// fresh matches ("hash2" opportunities -- wimlib's own bt_matchfinder.h
// uses the same technique, a single-slot most-recent-occurrence table) a
// candidate the main parse itself weighs against every other option every
// round, rather than an ex-post splice against an already-built Huffman
// table. An earlier version of this package (hash2greedy.go, removed
// 2026-08-18 -- see gowim's own TODO.md) tried the splice approach and
// found it could only ever accept a small fraction of a chunk's real
// length-2 opportunities: introducing any previously-zero-frequency
// symbol into a table that was already optimized without it is
// expensive by construction (a real Kraft-inequality effect, not a
// modeling bug), so the splice's own greedy MC>MB rule correctly
// rejected almost everything, unlike wimlib's real encoder, which treats
// length-2 matches as first-class from its very first pass.
//
// Unlike the length>=3 BST match finder, this needs no incremental
// insertion as the parse advances: byte VALUES never change based on
// which tokens get chosen, so the whole table can be computed once, up
// front, from data alone.
func buildHash2PrevOcc(data []byte) []int32 {
	n := len(data)
	lastPos := make([]int32, 1<<16)
	for i := range lastPos {
		lastPos[i] = -1
	}
	prevOcc := make([]int32, n)
	for i := range prevOcc {
		prevOcc[i] = -1 // the last position (p+1 == n) is never assigned by the loop below
	}
	for p := 0; p+1 < n; p++ {
		key := uint16(data[p]) | uint16(data[p+1])<<8
		prevOcc[p] = lastPos[key]
		lastPos[key] = int32(p)
	}
	return prevOcc
}

// hash2Candidate returns the length-2 fresh-match candidate at position i
// (prevOcc2[i]'s offset), or the zero value if none exists or if its
// offset would need a main-code symbol beyond nMainSyms. That specific
// boundary case (offset == windowSize-2, the maximum possible for a
// length-2 match) is not a bug to work around: the LZX format itself
// explicitly disallows it, precisely so the offset-slot table can be one
// slot smaller -- confirmed directly from wimlib's real
// lzx_get_num_main_syms (src/lzx_common.c), whose own comment states this
// outright ("the format disallows this case[,] reduc[ing] the number of
// needed offset slots by 1").
func hash2Candidate(prevOcc2 []int32, model costModel, litPrefix []int, nMainSyms int, i int) candidateMatch {
	q := prevOcc2[i]
	if q < 0 {
		return candidateMatch{}
	}
	offset := i - int(q)
	slot := offsetSlot(uint32(offset))
	if numChars+slot*numLenHeaders >= nMainSyms {
		return candidateMatch{}
	}
	extraBits := model.offsetExtraCost(slot, offset)
	return candidateMatch{
		found:  true,
		offset: offset,
		length: minMatchLen,
		repeat: -1,
		value:  model.matchValue(slot, minMatchLen, extraBits, litPrefix[i+minMatchLen]-litPrefix[i]),
	}
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
//
// findMatches is the default-tuned entry point; findMatchesWith takes the
// resolved caller-facing knobs (see Options in options.go) -- of which only
// MaxChainLen affects this parser, the rest being DP-specific.
func findMatches(data []byte, model costModel) []token {
	return findMatchesWith(data, model, defaultEncodeOptions())
}

func findMatchesWith(data []byte, model costModel, o encodeOptions) []token {
	n := len(data)
	if n == 0 {
		return nil
	}
	// Heuristic capacity hint: real-world data rarely averages worse than
	// one token per 4 bytes, so this avoids most of the slice growth
	// reallocations without affecting the tokens produced.
	toks := make([]token, 0, n/4+1)

	order, _ := windowOrder(n) // n > 0 here, and callers never exceed maxWindowSize
	nMainSyms := numMainSyms(order)
	prevOcc2 := buildHash2PrevOcc(data)

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
		return commonPrefixLen(data, c, pos, limit), limit
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
		for cur >= 0 && depth < o.maxChainLen {
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
		for cur >= 0 && depth < o.maxChainLen {
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
		return commonPrefixLen(data, i, j, limit)
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

	// litPrefix[j] is the total literalCost of data[0:j], so the real
	// summed literal cost of any range data[i:i+l] is litPrefix[i+l] -
	// litPrefix[i] in O(1) -- used by matchValue's litCost argument
	// instead of a flat length*flatLiteralBits guess (see literalCost's
	// own doc for why the flat guess is a meaningfully worse estimate).
	litPrefix := make([]int, n+1)
	for i := 0; i < n; i++ {
		litPrefix[i+1] = litPrefix[i] + model.literalCost(data[i])
	}

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
			v := model.matchValue(k, l, 0, litPrefix[i+l]-litPrefix[i])
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
		extraBits := model.offsetExtraCost(slot, bestOff)
		return candidateMatch{found: true, offset: bestOff, length: bestLen, repeat: -1, value: model.matchValue(slot, bestLen, extraBits, litPrefix[i+bestLen]-litPrefix[i])}
	}

	bestFreshCandidate2 := func(i int) candidateMatch {
		if i+minMatchLen > n {
			return candidateMatch{}
		}
		return hash2Candidate(prevOcc2, model, litPrefix, nMainSyms, i)
	}

	// chooseMatch finds the single best-value candidate (repeat or fresh,
	// including length-2 fresh matches via bestFreshCandidate2) at
	// position i, or the zero value if none is worth using over a
	// literal.
	chooseMatch := func(i int, queue [numRecentOffsets]int32) candidateMatch {
		best := bestRepeatCandidate(i, queue)
		if fresh := bestFreshCandidate(i); fresh.found && (!best.found || fresh.value > best.value) {
			best = fresh
		}
		if fresh2 := bestFreshCandidate2(i); fresh2.found && (!best.found || fresh2.value > best.value) {
			best = fresh2
		}
		if best.found && best.value <= 0 {
			return candidateMatch{}
		}
		return best
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

	// Bounded 4-way lookahead: at each position, evaluate the best repeat
	// candidate, the best length>=3 fresh candidate, the best length-2
	// fresh candidate (bestFreshCandidate2 -- see its own doc for why
	// this needs to be a native option here rather than an ex-post
	// splice), and "emit a literal", each combined with its own 1-step
	// continuation value (continuationValue above, itself a single
	// non-recursive chooseMatch call, so this stays a fixed depth-2
	// evaluation, never a full whole-chunk search) -- then commit
	// whichever option has the highest 2-step total. This is a
	// deliberately bounded generalization of one-step lazy matching
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

	// litLookahead caches the (rep, fresh, fresh2) triple computed for the
	// "emit a literal now" option's 1-step continuation at position i+1
	// (see the consider(literal) call below). When that literal option is
	// the one actually taken, the main loop's next iteration needs the
	// exact same triple at the exact same position with the exact same
	// queue (queue is untouched by a literal), and the BST has gained no
	// new insertions in between (only bstInsert(i) itself, which already
	// happened before this continuation was computed) -- so it is safe to
	// reuse rather than recompute. This does NOT extend to the rep/fresh/
	// fresh2 continuations evaluated at i+match.length: those speculative
	// evaluations run before insertRange(i, i+match.length) inserts the
	// intervening positions into the BST, so recomputing after the match
	// is committed can (correctly) find better fresh candidates that
	// weren't there yet.
	var litLookahead struct {
		valid              bool
		pos                int
		queue              [numRecentOffsets]int32
		rep, fresh, fresh2 candidateMatch
	}

	litContinuation := func(pos int, q [numRecentOffsets]int32) int {
		if pos >= n {
			litLookahead.valid = false
			return 0
		}
		r := bestRepeatCandidate(pos, q)
		f := bestFreshCandidate(pos)
		f2 := bestFreshCandidate2(pos)
		litLookahead = struct {
			valid              bool
			pos                int
			queue              [numRecentOffsets]int32
			rep, fresh, fresh2 candidateMatch
		}{true, pos, q, r, f, f2}

		best := r
		if f.found && (!best.found || f.value > best.value) {
			best = f
		}
		if f2.found && (!best.found || f2.value > best.value) {
			best = f2
		}
		if best.found && best.value <= 0 {
			return 0
		}
		return best.value
	}

	i := 0
	for i < n {
		var rep, fresh, fresh2 candidateMatch
		if litLookahead.valid && litLookahead.pos == i && litLookahead.queue == queue {
			rep, fresh, fresh2 = litLookahead.rep, litLookahead.fresh, litLookahead.fresh2
		} else {
			rep = bestRepeatCandidate(i, queue)
			fresh = bestFreshCandidate(i)
			fresh2 = bestFreshCandidate2(i)
		}
		litLookahead.valid = false
		// bestFreshCandidate (via bstSearch) must run before bstInsert(i):
		// inserting i's own tree entry first would let it match against
		// itself at offset 0. bestFreshCandidate2 doesn't use the BST at
		// all (see buildHash2PrevOcc), so ordering relative to bstInsert
		// doesn't matter for it.
		bstInsert(i)

		var best lookaheadOption
		have := false
		consider := func(o lookaheadOption) {
			if !have || o.total > best.total {
				best = o
				have = true
			}
		}

		consider(lookaheadOption{total: litContinuation(i+1, queue)}) // literal now
		if rep.found && rep.value > 0 {
			consider(lookaheadOption{cand: rep, total: rep.value + continuationValue(i+rep.length, applyMatch(queue, rep.repeat, rep.offset))})
		}
		if fresh.found && fresh.value > 0 {
			consider(lookaheadOption{cand: fresh, total: fresh.value + continuationValue(i+fresh.length, applyMatch(queue, fresh.repeat, fresh.offset))})
		}
		if fresh2.found && fresh2.value > 0 {
			consider(lookaheadOption{cand: fresh2, total: fresh2.value + continuationValue(i+fresh2.length, applyMatch(queue, fresh2.repeat, fresh2.offset))})
		}

		if !best.cand.found {
			toks = append(toks, token{literal: data[i], repeat: -1})
			i++
			continue
		}

		m := best.cand
		toks = append(toks, token{isMatch: true, offset: m.offset, length: m.length, repeat: m.repeat})
		queue = applyMatch(queue, m.repeat, m.offset)
		insertRange(i, i+m.length)
		i += m.length
	}

	return toks
}
