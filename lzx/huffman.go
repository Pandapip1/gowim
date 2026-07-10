package lzx

import "sort"

// This file implements canonical Huffman code construction (for the
// encoder) and decoding (for the decoder) shared by all four LZX Huffman
// alphabets (precode, main code, length code, aligned offset code).
//
// Symbol-to-codeword assignment follows the standard canonical-Huffman
// convention used throughout LZX/DEFLATE-family formats (and required for
// compatibility with wimlib's make_huffman_decode_table /
// make_canonical_huffman_code): codewords increase in value as symbol index
// increases, symbols are grouped by codeword length ascending, and shorter
// codewords sort numerically lower than any longer one with the same
// numeric prefix. This package does not attempt to match wimlib's internal
// fast-lookup table representation (include/wimlib/decompress_common.h) --
// only the resulting bitstream, which is what compatibility actually
// requires -- so decoding here uses the simpler, classic "first code per
// length" bit-at-a-time algorithm instead.

// buildLengths computes length-limited Huffman codeword lengths for the
// given per-symbol frequencies, with every length in [0, maxLen]. Symbols
// with zero frequency always get length 0 (unused). This does not attempt
// an optimal code (the package does not need one -- see lzx.go's scope
// notes) but does guarantee the length limit and a valid (Kraft-inequality
// satisfying) prefix code even for adversarial frequency distributions,
// using the same clamp-and-redistribute technique as zlib's trees.c
// (gen_bitlen's overflow-fixing loop) applied to a plain binary-heap
// Huffman tree.
func buildLengths(freqs []uint32, maxLen int) []byte {
	n := len(freqs)
	lens := make([]byte, n)

	type node struct {
		freq   uint64
		isLeaf bool
		sym    int // valid if isLeaf
		left   int // child index into nodes, if !isLeaf
		right  int
	}
	var nodes []node
	var heapIdx []int // indices into nodes, treated as a min-heap by freq

	less := func(i, j int) bool { return nodes[heapIdx[i]].freq < nodes[heapIdx[j]].freq }
	swap := func(i, j int) { heapIdx[i], heapIdx[j] = heapIdx[j], heapIdx[i] }
	push := func(idx int) {
		heapIdx = append(heapIdx, idx)
		i := len(heapIdx) - 1
		for i > 0 {
			p := (i - 1) / 2
			if !less(i, p) {
				break
			}
			swap(i, p)
			i = p
		}
	}
	pop := func() int {
		top := heapIdx[0]
		last := len(heapIdx) - 1
		heapIdx[0] = heapIdx[last]
		heapIdx = heapIdx[:last]
		i := 0
		for {
			l, r := 2*i+1, 2*i+2
			smallest := i
			if l < len(heapIdx) && less(l, smallest) {
				smallest = l
			}
			if r < len(heapIdx) && less(r, smallest) {
				smallest = r
			}
			if smallest == i {
				break
			}
			swap(i, smallest)
			i = smallest
		}
		return top
	}

	numUsed := 0
	for sym, f := range freqs {
		if f == 0 {
			continue
		}
		numUsed++
		idx := len(nodes)
		nodes = append(nodes, node{freq: uint64(f), isLeaf: true, sym: sym})
		push(idx)
	}

	if numUsed == 0 {
		return lens // no symbols used at all; all lengths stay 0
	}
	if numUsed == 1 {
		lens[nodes[heapIdx[0]].sym] = 1
		return lens
	}

	for len(heapIdx) > 1 {
		a := pop()
		b := pop()
		idx := len(nodes)
		nodes = append(nodes, node{freq: nodes[a].freq + nodes[b].freq, left: a, right: b})
		push(idx)
	}
	root := heapIdx[0]

	// Walk the tree to get raw (possibly-too-long) depths per symbol.
	depth := make([]int, n)
	var walk func(idx, d int)
	walk = func(idx, d int) {
		nd := &nodes[idx]
		if nd.isLeaf {
			depth[nd.sym] = d
			return
		}
		walk(nd.left, d+1)
		walk(nd.right, d+1)
	}
	walk(root, 0)

	// Build a length histogram, clamping anything over maxLen down into
	// bucket maxLen (creating a Kraft-inequality overflow to fix up next).
	// blCount is indexed by length 1..maxLen (index 0 unused).
	blCount := make([]int, maxLen+1)
	maxRawLen := 0
	for _, d := range depth {
		if d == 0 {
			continue // unused symbol
		}
		if d > maxRawLen {
			maxRawLen = d
		}
		l := d
		if l > maxLen {
			l = maxLen
		}
		blCount[l]++
	}

	if maxRawLen > maxLen {
		// Standard length-limiting fixup (as in zlib's trees.c): repeatedly
		// move one leaf from the deepest non-empty bucket below maxLen up
		// into maxLen, compensating by removing two units from maxLen's
		// bucket (this keeps the total leaf count constant while reducing
		// the Kraft sum until it satisfies the inequality for maxLen).
		overflow := 0
		for l := maxLen + 1; l <= 64 && l <= n; l++ {
			// depths beyond maxLen were already clamped into blCount[maxLen]
			// above; nothing to do here directly, but we still need to know
			// how many "excess" units exist. Recompute directly instead.
		}
		// Recompute overflow as: total Kraft numerator excess. Simplest
		// robust approach -- iteratively fix using bit-length counts only.
		// Count how many raw depths exceeded maxLen; each contributes one
		// unit that was folded into blCount[maxLen] but represents a
		// too-long codeword needing redistribution.
		for _, d := range depth {
			if d > maxLen {
				overflow++
			}
		}
		for overflow > 0 {
			l := maxLen - 1
			for l >= 1 && blCount[l] == 0 {
				l--
			}
			if l < 1 {
				// Degenerate (shouldn't happen for maxLen >= 2 with >1
				// symbol), bail out safely.
				break
			}
			blCount[l]--
			blCount[l+1] += 2
			blCount[maxLen]--
			overflow -= 2
		}
	}

	// Assign lengths to symbols: most frequent symbols get the shortest
	// available lengths, per the fixed-up blCount histogram.
	type symFreq struct {
		sym  int
		freq uint32
	}
	var used []symFreq
	for sym, f := range freqs {
		if f > 0 {
			used = append(used, symFreq{sym, f})
		}
	}
	sort.Slice(used, func(i, j int) bool {
		if used[i].freq != used[j].freq {
			return used[i].freq > used[j].freq
		}
		return used[i].sym < used[j].sym
	})
	pos := 0
	for l := 1; l <= maxLen; l++ {
		for c := 0; c < blCount[l]; c++ {
			lens[used[pos].sym] = byte(l)
			pos++
		}
	}
	// Any leftover (shouldn't happen) gets the max length as a fallback.
	for ; pos < len(used); pos++ {
		lens[used[pos].sym] = byte(maxLen)
	}

	return lens
}

// canonicalCodewords computes, for the given per-symbol codeword lengths,
// the canonical Huffman codeword for each symbol with a nonzero length.
// Symbols with length 0 get codeword 0 (unused; never written or read).
func canonicalCodewords(lens []byte, maxLen int) []uint16 {
	var count [65]int
	for _, l := range lens {
		if l > 0 {
			count[l]++
		}
	}
	var firstCode [66]uint32
	code := uint32(0)
	for l := 1; l <= maxLen; l++ {
		firstCode[l] = code
		code = (code + uint32(count[l])) << 1
	}
	next := firstCode
	codewords := make([]uint16, len(lens))
	for sym, l := range lens {
		if l == 0 {
			continue
		}
		codewords[sym] = uint16(next[l])
		next[l]++
	}
	return codewords
}

// huffDecoder decodes symbols for a canonical Huffman code given only the
// per-symbol codeword lengths, using the classic "first code / first symbol
// index per length" bit-at-a-time algorithm.
type huffDecoder struct {
	maxLen      int
	count       []int    // count[l] = number of codewords of length l
	firstCode   []uint32 // firstCode[l] = numeric value of the first codeword of length l
	firstSymIdx []int    // firstSymIdx[l] = index into symsByLen of the first symbol of length l
	symsByLen   []uint16 // symbols sorted by (length asc, symbol asc), grouped by length
}

func newHuffDecoder(lens []byte, maxLen int) *huffDecoder {
	d := &huffDecoder{
		maxLen:      maxLen,
		count:       make([]int, maxLen+1),
		firstCode:   make([]uint32, maxLen+1),
		firstSymIdx: make([]int, maxLen+1),
	}
	for _, l := range lens {
		if l > 0 {
			d.count[l]++
		}
	}
	code := uint32(0)
	idx := 0
	for l := 1; l <= maxLen; l++ {
		d.firstCode[l] = code
		d.firstSymIdx[l] = idx
		code = (code + uint32(d.count[l])) << 1
		idx += d.count[l]
	}
	d.symsByLen = make([]uint16, idx)
	next := append([]int(nil), d.firstSymIdx...)
	for sym, l := range lens {
		if l == 0 {
			continue
		}
		d.symsByLen[next[l]] = uint16(sym)
		next[l]++
	}
	return d
}

// decode reads one Huffman-encoded symbol from r. If the code is degenerate
// (no symbols at all), it returns 0, false.
func (d *huffDecoder) decode(r *bitReader) (uint16, bool) {
	code := uint32(0)
	for l := 1; l <= d.maxLen; l++ {
		code = (code << 1) | r.readBits(1)
		if d.count[l] > 0 {
			rel := code - d.firstCode[l]
			if rel < uint32(d.count[l]) {
				return d.symsByLen[d.firstSymIdx[l]+int(rel)], true
			}
		}
	}
	return 0, false
}
