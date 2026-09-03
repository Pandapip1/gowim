package lzx

import "sort"

// symFreqDesc pairs a symbol with its frequency for the length-assignment
// sort in buildLengths.
type symFreqDesc struct {
	sym  int
	freq uint32
}

// byFreqDescSymAsc implements sort.Interface for []symFreqDesc, ordering by
// descending frequency and, for ties, ascending symbol number. A concrete
// sort.Interface avoids the reflection/closure overhead of sort.Slice on
// this hot per-block path.
type byFreqDescSymAsc []symFreqDesc

func (s byFreqDescSymAsc) Len() int      { return len(s) }
func (s byFreqDescSymAsc) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byFreqDescSymAsc) Less(i, j int) bool {
	if s[i].freq != s[j].freq {
		return s[i].freq > s[j].freq
	}
	return s[i].sym < s[j].sym
}

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
		// A single used symbol only needs one codeword, but a valid
		// (complete) Huffman code needs two codewords at the smallest
		// length. wimlib's make_canonical_huffman_code (src/compress_common.c)
		// handles this by assigning the unused unused codeword to symbol 0
		// (or symbol 1, if the used symbol is 0), so that the lower-valued
		// symbol still gets codeword 0 -- keeping the code canonical. This
		// matters for real-decoder compatibility: wimlib's decode-table
		// builder (src/decompress_common.c, make_huffman_decode_table)
		// rejects any incomplete code that isn't completely empty, so a
		// single-symbol code with only one codeword assigned is invalid and
		// was observed to be rejected by wimlib-imagex (confirmed via
		// direct libwim wimlib_decompress calls during development).
		sym := nodes[heapIdx[0]].sym
		other := 0
		if sym == 0 {
			other = 1
		}
		lens[sym] = 1
		lens[other] = 1
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
	used := make([]symFreqDesc, 0, len(freqs))
	for sym, f := range freqs {
		if f > 0 {
			used = append(used, symFreqDesc{sym, f})
		}
	}
	sort.Sort(byFreqDescSymAsc(used))
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
	maxLen int
	// count/firstCode/firstSymIdx are fixed-size arrays rather than
	// slices: every call site passes one of the package's four
	// maxLen constants (maxMainCodewordLen, maxLenCodewordLen,
	// maxPrecodeCodewordLen, maxAlignedCodewordLen), all <=
	// maxMainCodewordLen (16), so sizing them to the largest possible
	// maxLen+1 lets them live inline in *huffDecoder's own allocation
	// instead of needing 3 separate slice allocations per call.
	count       [maxMainCodewordLen + 1]int    // count[l] = number of codewords of length l
	firstCode   [maxMainCodewordLen + 1]uint32 // firstCode[l] = numeric value of the first codeword of length l
	firstSymIdx [maxMainCodewordLen + 1]int    // firstSymIdx[l] = index into symsByLen of the first symbol of length l
	symsByLen   []uint16                       // symbols sorted by (length asc, symbol asc), grouped by length
}

func newHuffDecoder(lens []byte, maxLen int) *huffDecoder {
	d := &huffDecoder{maxLen: maxLen}
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
	next := d.firstSymIdx // [maxMainCodewordLen+1]int is a value type: this copies it
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
	// Ensure once for the whole walk (d.maxLen <= 16, well under
	// ensure's 32-bit limit) and do the per-bit work against local
	// copies of r.buf/r.nbits, writing them back only once a symbol is
	// found (or the loop exhausts maxLen). buf>>63 then buf<<=1 is
	// exactly what peek(1) then remove(1) compute, so this is bit-for-
	// bit identical to calling r.readBits(1) in the loop -- just without
	// re-entering ensure/peek/remove and round-tripping through r's
	// fields on every one of up to maxLen iterations.
	r.ensure(uint(d.maxLen))
	buf, nbits := r.buf, r.nbits
	code := uint32(0)
	for l := 1; l <= d.maxLen; l++ {
		code = (code << 1) | uint32(buf>>63)
		buf <<= 1
		nbits--
		if d.count[l] > 0 {
			rel := code - d.firstCode[l]
			if rel < uint32(d.count[l]) {
				r.buf, r.nbits = buf, nbits
				return d.symsByLen[d.firstSymIdx[l]+int(rel)], true
			}
		}
	}
	r.buf, r.nbits = buf, nbits
	return 0, false
}
