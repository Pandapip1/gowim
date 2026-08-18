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

	// repeatBonus is how many bytes shorter a repeat-offset match is allowed
	// to be than the best fresh-offset match while still being preferred.
	// Reusing one of the three most-recent offsets costs zero extra offset
	// bits (lzxExtraOffsetBits[0..2] are all 0) and no offset-slot Huffman
	// code beyond the length header, whereas a fresh offset can cost up to
	// 17 extra bits plus a costlier main-code symbol; a repeat match within
	// a couple of bytes of the best fresh match is essentially always
	// cheaper once Huffman-coded. This fixed margin is a conservative
	// heuristic (matching the kind of tradeoff wimlib's own non-optimal-
	// parse compression levels make), not a full bit-cost model, consistent
	// with this package's documented greedy/lazy-parse scope.
	repeatBonus = 2
)

// candidateMatch is the single best match (repeat-offset or fresh) found at
// one position, as chosen by chooseMatch below.
type candidateMatch struct {
	found  bool
	offset int
	length int
	repeat int // -1 if fresh
}

// findMatches runs a lazy LZ77 parse over data using a bounded hash-chain
// match finder for fresh offsets, plus a direct check of the LZX
// repeat-offset LRU queue (the three most recently used match offsets) at
// every position. Per this package's documented encoder scope (see lzx.go
// and compress() in encode.go), this is a one-step lazy parse, not a full
// optimal/DP parse: at each position it finds the best match (preferring a
// repeat-offset match within repeatBonus bytes of the best fresh match,
// since it is cheaper to encode), then checks whether a strictly longer
// match exists starting one byte later using the *same* repeat-offset queue
// state (valid because emitting a literal never changes the queue) -- if
// so, it emits a literal now and takes the better match next iteration,
// exactly the classic "lazy matching" technique (as used by, e.g., zlib's
// deflate and wimlib's own non-near-optimal compression levels), rather
// than a full iterative bit-cost model.
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
func findMatches(data []byte) []token {
	n := len(data)
	var toks []token
	if n == 0 {
		return toks
	}

	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, n)
	inserted := make([]bool, n)

	hash := func(i int) uint32 {
		v := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16
		return (v * 2654435761) >> (32 - hashBits)
	}

	// insertOnce records position i's hash entry the first time any code
	// path visits it, whether it ends up part of a match or a literal, so a
	// later lazy-matching peek at position i+1 can find i as a candidate.
	// It is idempotent since the lazy peek at i+1 and the real processing
	// of i+1 (once the loop cursor reaches it) would otherwise both try to
	// insert it.
	insertOnce := func(i int) {
		if i+3 > n || inserted[i] {
			return
		}
		inserted[i] = true
		h := hash(i)
		prev[i] = head[h]
		head[h] = int32(i)
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

	// chooseMatch finds the best match at position i given the current
	// queue, without mutating any state (hash table, chain, or queue), so
	// it is safe to call speculatively for a lazy-matching peek.
	chooseMatch := func(i int, queue [numRecentOffsets]int32) candidateMatch {
		bestRepLen := 0
		bestRepIdx := -1
		for k := 0; k < numRecentOffsets; k++ {
			l := repeatLenAt(i, queue[k])
			if l > bestRepLen {
				bestRepLen = l
				bestRepIdx = k
			}
		}

		bestLen := 0
		bestOff := 0
		if i+3 <= n {
			h := hash(i)
			cand := head[h]
			depth := 0
			for cand >= 0 && depth < maxChainLen {
				c := int(cand)
				l := matchLenAt(c, i)
				if l > bestLen {
					bestLen = l
					bestOff = i - c
				}
				cand = prev[c]
				depth++
			}
		}

		switch {
		case bestRepIdx >= 0 && bestRepLen >= minMatchLen && bestRepLen+repeatBonus >= bestLen:
			return candidateMatch{found: true, offset: int(queue[bestRepIdx]), length: bestRepLen, repeat: bestRepIdx}
		case bestLen >= minMatch:
			return candidateMatch{found: true, offset: bestOff, length: bestLen, repeat: -1}
		default:
			return candidateMatch{}
		}
	}

	advanceQueue := func(m candidateMatch) {
		if m.repeat >= 0 {
			if m.repeat != 0 {
				used := queue[m.repeat]
				queue[m.repeat] = queue[0]
				queue[0] = used
			}
			return
		}
		queue[2] = queue[1]
		queue[1] = queue[0]
		queue[0] = int32(m.offset)
	}

	insertRange := func(start, end int) {
		for p := start; p < end; p++ {
			insertOnce(p)
		}
	}

	i := 0
	for i < n {
		// chooseMatch must run before insertOnce(i): inserting i's own hash
		// entry first would let it match against itself at offset 0.
		m := chooseMatch(i, queue)
		insertOnce(i)

		if m.found && i+1 < n {
			peek := chooseMatch(i+1, queue)
			if peek.found && peek.length > m.length {
				toks = append(toks, token{literal: data[i], repeat: -1})
				i++
				continue
			}
		}

		if !m.found {
			toks = append(toks, token{literal: data[i], repeat: -1})
			i++
			continue
		}

		toks = append(toks, token{isMatch: true, offset: m.offset, length: m.length, repeat: m.repeat})
		advanceQueue(m)
		insertRange(i, i+m.length)
		i += m.length
	}

	return toks
}
