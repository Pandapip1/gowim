package lzx

// token is one literal or match emitted by the greedy match finder.
type token struct {
	isMatch bool
	literal byte
	offset  int // match distance in bytes (>= 1)
	length  int // match length in bytes ([minMatchLen, maxMatchLen])
}

const (
	hashBits    = 16
	hashSize    = 1 << hashBits
	minMatch    = 3 // this encoder never emits length-2 matches (see below)
	maxChainLen = 96
)

// findMatches runs a simple greedy LZ77 parse over data using a bounded
// hash-chain match finder, splitting each match longer than maxMatchLen
// into consecutive matches. Per this package's documented encoder scope
// (see lzx.go and compress() in encode.go), this is intentionally a plain
// greedy parse (no lazy matching, no optimal parse) and never emits a
// length-2 match: length-2 matches only pay off for very small offsets and
// require extra bookkeeping (LZX's repeat-offset queue) to be worthwhile,
// which this encoder does not implement -- literals are used instead,
// which is always correct, only sometimes less compact.
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

	i := 0
	for i < n {
		if i+3 > n {
			toks = append(toks, token{literal: data[i]})
			i++
			continue
		}

		h := hash(i)
		cand := head[h]
		bestLen := 0
		bestOff := 0
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

		if bestLen >= minMatch {
			// Emit the match, splitting if longer than maxMatchLen (already
			// capped by matchLenAt) -- and insert hash entries for every
			// position covered so later matches can reference into it.
			remaining := bestLen
			for remaining > 0 {
				l := remaining
				if l > maxMatchLen {
					l = maxMatchLen
				}
				if l < minMatchLen {
					// Shouldn't happen since bestLen >= minMatch >
					// minMatchLen, but guard defensively.
					break
				}
				toks = append(toks, token{isMatch: true, offset: bestOff, length: l})
				remaining -= l
			}
			end := i + bestLen
			for ; i < end; i++ {
				if i+3 <= n {
					insert(i)
				}
			}
		} else {
			toks = append(toks, token{literal: data[i]})
			insert(i)
			i++
		}
	}

	return toks
}
