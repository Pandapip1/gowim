package lzx

// token is one literal or match emitted by the greedy match finder.
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
	// with this package's documented greedy-parse scope.
	repeatBonus = 2
)

// findMatches runs a greedy LZ77 parse over data using a bounded hash-chain
// match finder for fresh offsets, plus a direct check of the LZX
// repeat-offset LRU queue (the three most recently used match offsets) at
// every position. Per this package's documented encoder scope (see lzx.go
// and compress() in encode.go), this is intentionally a plain greedy parse
// (no lazy matching, no optimal parse): at each position it takes the
// longer of "best fresh-offset match" and "best repeat-offset match" (with a
// small bonus favoring the repeat, since it is cheaper to encode -- see
// repeatBonus above), never looking ahead to see whether a literal now would
// let a better match start one byte later.
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

	hash := func(i int) uint32 {
		v := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16
		return (v * 2654435761) >> (32 - hashBits)
	}

	insert := func(i int) {
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

	// queue mirrors the decoder's recentOffsets, starting at {1, 1, 1} (see
	// decode.go). A repeat-offset match at position i and queue value off
	// references data[i-off:], which is only valid when off <= i.
	var queue [numRecentOffsets]int32 = [numRecentOffsets]int32{1, 1, 1}

	repeatLenAt := func(i int, off int32) int {
		j := i - int(off)
		if j < 0 {
			return 0
		}
		return matchLenAt(j, i)
	}

	insertRange := func(start, end int) {
		for p := start; p < end; p++ {
			if p+3 <= n {
				insert(p)
			}
		}
	}

	i := 0
	for i < n {
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
			toks = append(toks, token{isMatch: true, offset: int(queue[bestRepIdx]), length: bestRepLen, repeat: bestRepIdx})
			if bestRepIdx != 0 {
				used := queue[bestRepIdx]
				queue[bestRepIdx] = queue[0]
				queue[0] = used
			}
			insertRange(i, i+bestRepLen)
			i += bestRepLen

		case bestLen >= minMatch:
			toks = append(toks, token{isMatch: true, offset: bestOff, length: bestLen, repeat: -1})
			queue[2] = queue[1]
			queue[1] = queue[0]
			queue[0] = int32(bestOff)
			insertRange(i, i+bestLen)
			i += bestLen

		default:
			toks = append(toks, token{literal: data[i], repeat: -1})
			if i+3 <= n {
				insert(i)
			}
			i++
		}
	}

	return toks
}
