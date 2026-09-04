package lzx

// This file holds the two interchangeable fresh-offset match-finder
// structures the bounded-lookahead parser (findMatchesWith in matcher.go)
// can be built on, plus the single-descent search/insert routines each of
// them exposes. Both are selected once per parse, in findMatchesWith, and
// bound to the same three closures (searchOnly, searchInsert, insertOnly),
// so nothing downstream of that point has to know which structure is in
// use. See Options.HashChainMatcher in options.go for what each one costs
// and buys.
//
// Both structures share the caller's head table (one entry per hash bucket)
// and the caller's hash function, and both index only positions with at
// least minLook bytes remaining (the bytes that hash consumes).

// newBSTMatcher builds the binary-search-tree match finder: this package's
// default, and the structure it has used since the plain hash chain it
// originally shipped with was replaced. A BST gives meaningfully better
// candidates within the same bounded comparison budget (o.maxChainLen),
// since it can discard whole subtrees known to be lexicographically on the
// wrong side rather than walking a flat recency list -- the same technique
// real encoders' "bt" match finders use (e.g. the LZMA SDK's bt4); see
// gowim's own TODO.md for the measured improvement that made.
//
// The tree is rooted at head[hash(pos)] and ordered lexicographically by
// each position's suffix. Node p's two children are interleaved into one
// array as sons[2*p] (left/lower) and sons[2*p+1] (right/higher), matching
// the LZMA SDK's own bt4 layout: a descent step reads one child of the
// current node and then writes the *other* slot (the new node's sibling
// pointer), so keeping both children in the same cache line halves the
// cache lines touched per step versus two separate left/right slices.
//
// # Why there is no lenLo/lenHi prefix skipping here
//
// LZMA's bt4 descent (GetMatchesSpec1, C/LzFind.c) carries two extra
// values down the descent: the common-prefix length guaranteed with
// everything remaining on the low side (lenLo) and on the high side
// (lenHi), so each node's comparison can start at min(lenLo, lenHi)
// instead of at byte 0. This package deliberately does not do that, and
// the reason is measured, not assumed.
//
// It is not that the invariant fails. It does need one change to hold: the
// descent below folds "this candidate matched all the way to limit" into
// its go-right branch (that is what the l >= limit short-circuit does, and
// it is also what keeps the data[pos+l] read below in bounds), and that
// rule is not a consistent total order across positions, since limit is
// min(maxMatchLen, n-pos) and so differs per position. Doing what LZMA
// does on that case instead -- stop the descent there and splice the tied
// node's two subtrees in as the new node's own children, which loses no
// search quality, since a candidate matching to limit is already the
// longest match this position can have and the nearer of two identical
// prefixes dominates the older one for every future search -- makes every
// comparison the descent performs a strict lexicographic one, and then the
// classic argument goes through: after a right-step at a node whose common
// prefix with pos was l, everything still reachable sorts above that node
// and below the bound the earlier left-steps established, so it shares at
// least l bytes with pos.
//
// That whole variant was implemented and measured (2026-09-03, on this
// package's matchfinder_test.go corpus -- 18 chunks, 585,216 bytes -- on an
// otherwise-idle i9-12900K):
//
//   - It is correct. Holding the tree shape fixed (the terminate-at-limit
//     rule above in both runs) and toggling only the skipping produced
//     byte-identical compressed output over the whole corpus, at both hash
//     widths and with the DP parser both on and off. A violated invariant
//     is exactly what that comparison would have caught, since a wrong skip
//     silently yields a different (still valid, still decodable) match
//     rather than corrupt output.
//   - It skips as advertised. Instrumented over the corpus at MaxChainLen
//     96, the read-only search descents compared 27,397,954 bytes without it
//     and
//     13,863,342 with it, across the identical 2,882,439 node visits: it
//     removes 49% of the match finder's byte comparisons.
//   - It makes nothing meaningfully faster. Whole-corpus encode time, mean
//     of 16 runs each: MaxChainLen 16 (what Fast uses) 234.1ms vs 237.7ms,
//     skipping SLOWER; MaxChainLen 96 258.6ms vs 256.9ms; MaxChainLen 512,
//     where descents are deepest and it should pay most, 263.8ms vs
//     255.0ms, a 3.3% win at a search depth no preset uses and that is
//     itself 13% slower than the depth they do use. Total output size was
//     identical to the byte at every depth tried.
//
// The explanation is commonPrefixLen (matchlen.go): comparisons here are
// already 8 bytes per load via SWAR, so 27M compared bytes is only ~1.2
// loads per node visit to begin with, and the per-node cost is dominated by
// the dependent, cache-missing load of the next node's child pointers, not
// by the bytes compared. Skipping comparisons that were nearly free buys
// nothing and costs a little bookkeeping on every node. It is kept out for
// the same reason matchlen.go's AVX2 variant was: an alternative that
// measures no better should not exist for someone to find and enable.
func newBSTMatcher(data []byte, o encodeOptions, head []int32, hash func(int) uint32, minLook int) (searchOnly, searchInsert func(int) (int, int), insertOnly func(int)) {
	n := len(data)
	sons := make([]int32, 2*n)

	// limitAt is the match-length cap for pos, hoisted out of the per-node
	// loops below rather than recomputed per node: every candidate c in the
	// tree at the time of any search satisfies c < pos (positions are only
	// ever inserted in increasing order, and always strictly before the
	// position being searched), so n-c > n-pos always, and the "cap to
	// buffer end" limit is therefore always n-pos, never n-c.
	limitAt := func(pos int) int {
		limit := n - pos
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		return limit
	}

	// searchOnly walks the tree without touching it, so it stays safe to
	// call speculatively for the parser's lazy-matching peeks. Being
	// read-only it can also stop the instant bestLen reaches limit: limit is
	// shared by every candidate (see limitAt), so no later one can beat it.
	// That early exit is NOT available to searchInsert below, which must
	// finish its descent regardless of bestLen in order to rewire the tree.
	searchOnly = func(pos int) (bestLen, bestOff int) {
		if pos+minLook > n {
			return 0, 0
		}
		limit := limitAt(pos)
		cur := head[hash(pos)]
		for depth := 0; cur >= 0 && depth < o.maxChainLen; depth++ {
			c := int(cur)
			l := commonPrefixLen(data, c, pos, limit)
			if l > bestLen {
				bestLen, bestOff = l, pos-c
				if bestLen == limit {
					break
				}
			}
			if l >= limit || data[c+l] < data[pos+l] {
				cur = sons[2*c+1]
			} else {
				cur = sons[2*c]
			}
		}
		return bestLen, bestOff
	}

	// searchInsert fuses that search with linking pos into the tree, per the
	// standard "insert while descending" binary-tree match finder: pos
	// becomes the new root of its bucket, with the old tree split into pos's
	// left/right subtrees by lexicographic comparison as the descent
	// proceeds. The search half still sees the tree as it was before pos was
	// added (pos is only ever linked in through the dangling ptrLo/ptrHi
	// left behind by the loop), so it can never match against itself at
	// offset 0. Only valid where the two operations are genuinely
	// back-to-back with no intervening mutation -- the main loop's own
	// committed position -- never at the speculative call sites, which must
	// stay non-mutating.
	searchInsert = func(pos int) (bestLen, bestOff int) {
		if pos+minLook > n {
			return 0, 0
		}
		limit := limitAt(pos)
		h := hash(pos)
		cur := head[h]
		ptrLo := &sons[2*pos]
		ptrHi := &sons[2*pos+1]
		for depth := 0; cur >= 0 && depth < o.maxChainLen; depth++ {
			c := int(cur)
			l := commonPrefixLen(data, c, pos, limit)
			if l > bestLen {
				bestLen, bestOff = l, pos-c
			}
			if l >= limit || data[c+l] < data[pos+l] {
				*ptrLo = int32(c)
				ptrLo = &sons[2*c+1]
				cur = sons[2*c+1]
			} else {
				*ptrHi = int32(c)
				ptrHi = &sons[2*c]
				cur = sons[2*c]
			}
		}
		*ptrLo = -1
		*ptrHi = -1
		head[h] = int32(pos)
		return bestLen, bestOff
	}

	// A BST insertion is a full descent no matter what, since the tree has
	// to be rewired; there is no cheaper insert-only path to offer.
	insertOnly = func(pos int) { _, _ = searchInsert(pos) }
	return searchOnly, searchInsert, insertOnly
}

// newChainMatcher builds the hash-chain match finder: prev[p] is the
// previous position in p's own bucket, so a bucket is a singly linked list
// of positions in strict recency (decreasing position) order, and an
// insertion is one store plus one head update rather than a whole tree
// descent with pointer rewiring.
//
// This is deliberately a weaker search than the BST at the same
// o.maxChainLen: it walks a flat recency list and cannot discard whole
// subtrees, so within the same comparison budget it can miss the longer
// match a tree would have found. What it buys is that each of those
// comparisons is far cheaper -- no rewiring, half the per-position memory
// (one int32 per position rather than two, which also means half the
// cache-missing traffic per node visited), and, critically, two filters a
// tree cannot use:
//
//   - The "can this candidate even beat what I have" test: a candidate can
//     only improve on bestLen if its byte AT bestLen already matches pos's,
//     which is one load and one compare against the tail of the current
//     best, and it rejects the large majority of chain entries before
//     commonPrefixLen is called at all. This is the classic zlib/LZ4 chain
//     filter, and it is exact rather than heuristic: any candidate it
//     rejects provably cannot produce l > bestLen.
//   - Early exit at limit in the *inserting* path too. The BST's inserting
//     descent must run to completion regardless of bestLen because it is
//     simultaneously rebuilding the tree; a chain insertion is a prepend
//     that has already happened by then, so the walk can stop the moment
//     the best possible match is in hand. On run-heavy data, where every
//     match is length-capped, that is most of the work.
//
// See Options.HashChainMatcher in options.go for the measured size/speed
// result and Fast's doc for why that trade is worth taking there.
func newChainMatcher(data []byte, o encodeOptions, head []int32, hash func(int) uint32, minLook int) (searchOnly, searchInsert func(int) (int, int), insertOnly func(int)) {
	n := len(data)
	prev := make([]int32, n)

	// walk is the shared body of both search entry points: everything after
	// the bucket head has been read (and, for searchInsert, after pos has
	// already been prepended -- which is safe, since the walk starts from
	// the head value read *before* that prepend, so pos never sees itself).
	walk := func(pos, limit int, cur int32) (bestLen, bestOff int) {
		for depth := 0; cur >= 0 && depth < o.maxChainLen; depth++ {
			c := int(cur)
			// bestLen < limit is guaranteed here (the loop returns as soon
			// as they become equal), so pos+bestLen and c+bestLen are both
			// in bounds.
			if bestLen > 0 && data[c+bestLen] != data[pos+bestLen] {
				cur = prev[c]
				continue
			}
			if l := commonPrefixLen(data, c, pos, limit); l > bestLen {
				bestLen, bestOff = l, pos-c
				if bestLen == limit {
					return bestLen, bestOff
				}
			}
			cur = prev[c]
		}
		return bestLen, bestOff
	}

	limitAt := func(pos int) int {
		limit := n - pos
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		return limit
	}

	searchOnly = func(pos int) (int, int) {
		if pos+minLook > n {
			return 0, 0
		}
		return walk(pos, limitAt(pos), head[hash(pos)])
	}

	searchInsert = func(pos int) (int, int) {
		if pos+minLook > n {
			return 0, 0
		}
		h := hash(pos)
		cur := head[h]
		prev[pos] = cur
		head[h] = int32(pos)
		return walk(pos, limitAt(pos), cur)
	}

	insertOnly = func(pos int) {
		if pos+minLook > n {
			return
		}
		h := hash(pos)
		prev[pos] = head[h]
		head[h] = int32(pos)
	}
	return searchOnly, searchInsert, insertOnly
}
