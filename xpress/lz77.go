package xpress

// lz77.go implements a simple greedy/lazy LZ77 match finder used by the
// encoder (encode.go). It is deliberately straightforward: a hash-chain
// match finder with a bounded search depth and a single position of
// lookahead (lazy matching). This is not intended to match wimlib's
// near-optimal parser in compression ratio -- see the package doc in
// xpress.go for why that is out of scope here.

// maxSearchDepth bounds how many candidates are examined per hash chain,
// trading ratio for speed. Kept small since ratio is a non-goal.
const maxSearchDepth = 64

// hashBits controls the match-finder hash table size (1<<hashBits entries).
const hashBits = 16

// item is a single parsed literal or match, in the order they should be
// written to the output.
type item struct {
	isMatch bool
	literal byte
	length  int // valid match length (>= minMatchLen), only if isMatch
	offset  int // match distance (>= minOffset), only if isMatch
}

// matchFinder is a hash-chain-based LZ77 match finder over a fixed buffer.
type matchFinder struct {
	data []byte
	head []int32 // hashBits-indexed table of most recent position, or -1
	prev []int32 // per-position link to the previous position with the same hash, or -1
}

func newMatchFinder(data []byte) *matchFinder {
	head := make([]int32, 1<<hashBits)
	for i := range head {
		head[i] = -1
	}
	return &matchFinder{
		data: data,
		head: head,
		prev: make([]int32, len(data)),
	}
}

func hash3(b0, b1, b2 byte) uint32 {
	v := uint32(b0) | uint32(b1)<<8 | uint32(b2)<<16
	return (v * 2654435761) >> (32 - hashBits)
}

// insert adds position i to the hash chain so that later calls to findMatch
// can find it as a candidate.
func (mf *matchFinder) insert(i int) {
	if i+3 > len(mf.data) {
		return
	}
	h := hash3(mf.data[i], mf.data[i+1], mf.data[i+2])
	mf.prev[i] = mf.head[h]
	mf.head[h] = int32(i)
}

// findMatch searches for the longest match starting at position i against
// data already inserted into the hash chains (i.e. earlier positions). It
// returns (length, offset); length is 0 if no match of at least
// minMatchLen was found.
func (mf *matchFinder) findMatch(i int) (length, offset int) {
	data := mf.data
	n := len(data)
	if i+3 > n {
		return 0, 0
	}
	limit := n - i
	if limit > maxMatchLen {
		limit = maxMatchLen
	}

	h := hash3(data[i], data[i+1], data[i+2])
	cand := mf.head[h]
	depth := 0
	bestLen := 0
	bestOff := 0
	for cand >= 0 && depth < maxSearchDepth {
		c := int(cand)
		off := i - c
		if off > maxOffset {
			break
		}
		if off >= minOffset {
			l := matchLength(data, c, i, limit)
			if l > bestLen {
				bestLen = l
				bestOff = off
				if bestLen >= limit {
					break
				}
			}
		}
		cand = mf.prev[c]
		depth++
	}
	if bestLen < minMatchLen {
		return 0, 0
	}
	return bestLen, bestOff
}

// matchLength returns how many bytes starting at src and dst (dst > src)
// match, up to limit bytes.
func matchLength(data []byte, src, dst, limit int) int {
	l := 0
	for l < limit && data[src+l] == data[dst+l] {
		l++
	}
	return l
}

// parseItems runs the greedy/lazy LZ77 parse over the whole input buffer,
// producing the sequence of literals and matches to encode.
func parseItems(data []byte) []item {
	n := len(data)
	if n == 0 {
		return nil
	}
	items := make([]item, 0, n)
	mf := newMatchFinder(data)

	i := 0
	for i < n {
		length, offset := mf.findMatch(i)
		if length >= minMatchLen {
			mf.insert(i)
			if i+1 < n {
				length2, _ := mf.findMatch(i + 1)
				if length2 > length {
					// A longer match starts one byte later; emit a
					// literal now and let the next iteration pick up
					// the better match (classic lazy matching).
					items = append(items, item{literal: data[i]})
					i++
					continue
				}
			}
			items = append(items, item{isMatch: true, length: length, offset: offset})
			end := i + length
			if end > n {
				end = n
			}
			for k := i + 1; k < end; k++ {
				mf.insert(k)
			}
			i = end
		} else {
			mf.insert(i)
			items = append(items, item{literal: data[i]})
			i++
		}
	}
	return items
}
