package lzms

import "sort"

// This file implements LZMS's canonical Huffman code construction and
// adaptive rebuild scheme, ported from make_canonical_huffman_code() in
// wimlib's src/compress_common.c together with the surrounding
// lzms_build_huffman_code() / lzms_init_huffman_code() /
// lzms_rebuild_huffman_code() logic in lzms_decompress.c / lzms_compress.c
// (see lzms.go for the exact source commit).
//
// LZMS requires bit-for-bit reproducibility of the Huffman code derived
// from a set of symbol frequencies: given the same frequencies, both the
// encoder and decoder must independently construct the exact same
// canonical code, including a specific tie-breaking rule (favor leaves
// over internal nodes when frequencies are equal) inherited from the
// classic Huffman-tree construction algorithm. This file replicates that
// tie-breaking rule precisely (via buildTree/computeLengthCounts, ported
// from wimlib's build_tree()/compute_length_counts()), rather than using
// an arbitrary alternative canonical-code construction, so that this
// package can decode real LZMS streams produced by wimlib/Microsoft, whose
// adaptive codes depend on exactly this tie-break behavior.

const maxCodewordLen = 15 // LZMS_MAX_CODEWORD_LENGTH

// huffmanCode is one of LZMS's five adaptive Huffman-coded alphabets
// (literal, LZ offset, length, delta offset, delta power).
type huffmanCode struct {
	numSyms      int
	rebuildFreq  int
	untilRebuild int

	freqs     []uint32
	lens      []uint8
	codewords []uint32

	// Decoding aids, derived from lens/codewords after each (re)build.
	firstCode    [maxCodewordLen + 2]uint32
	firstSymIdx  [maxCodewordLen + 2]int
	symsByLength []int // symbols with nonzero length, grouped by length ascending then symbol ascending
}

func newHuffmanCode(numSyms, rebuildFreq int) *huffmanCode {
	// lens/codewords are allocated with room for at least 2 entries: when
	// only one symbol has nonzero frequency, buildCanonicalHuffmanCode
	// must still assign a dummy codeword to a second, otherwise-unused
	// symbol index so that the resulting length-1 code is complete (see
	// the numUsed == 1 special case below, ported from
	// make_canonical_huffman_code()). wimlib's C arrays are always
	// allocated at a fixed maximum size regardless of the logical
	// alphabet size in play, which is how it avoids this out-of-bounds
	// concern; we replicate that headroom explicitly instead.
	allocSize := numSyms
	if allocSize < 2 {
		allocSize = 2
	}
	h := &huffmanCode{
		numSyms:      numSyms,
		rebuildFreq:  rebuildFreq,
		freqs:        make([]uint32, numSyms),
		lens:         make([]uint8, allocSize),
		codewords:    make([]uint32, allocSize),
		symsByLength: make([]int, 0, numSyms),
	}
	for i := range h.freqs {
		h.freqs[i] = 1 // lzms_init_symbol_frequencies
	}
	h.rebuild()
	return h
}

func (h *huffmanCode) rebuild() {
	buildCanonicalHuffmanCode(h.numSyms, maxCodewordLen, h.freqs, h.lens, h.codewords)
	h.buildDecodeAids()
	h.untilRebuild = h.rebuildFreq
}

func (h *huffmanCode) dilute() {
	for i := range h.freqs {
		h.freqs[i] = (h.freqs[i] >> 1) + 1 // lzms_dilute_symbol_frequencies
	}
}

// buildDecodeAids constructs, from h.lens, the per-length "first codeword"
// table and the symbol list (ordered by length then symbol value) needed
// to decode a canonical Huffman code bit by bit.
func (h *huffmanCode) buildDecodeAids() {
	var lenCounts [maxCodewordLen + 2]int
	for _, l := range h.lens[:h.numSyms] {
		if l > 0 {
			lenCounts[l]++
		}
	}

	h.symsByLength = h.symsByLength[:0]
	var firstSymIdx [maxCodewordLen + 2]int
	idx := 0
	for l := 1; l <= maxCodewordLen; l++ {
		firstSymIdx[l] = idx
		idx += lenCounts[l]
	}
	// Fill in symbols, ascending by symbol value within each length
	// (matches gen_codewords' assignment order).
	cursor := firstSymIdx
	tmp := make([]int, idx)
	for sym := 0; sym < h.numSyms; sym++ {
		l := h.lens[sym]
		if l == 0 {
			continue
		}
		tmp[cursor[l]] = sym
		cursor[l]++
	}
	h.symsByLength = append(h.symsByLength, tmp...)
	h.firstSymIdx = firstSymIdx

	var firstCode [maxCodewordLen + 2]uint32
	firstCode[0] = 0
	firstCode[1] = 0
	for l := 2; l <= maxCodewordLen+1; l++ {
		firstCode[l] = (firstCode[l-1] + uint32(lenCounts[l-1])) << 1
	}
	h.firstCode = firstCode
}

// decodeSymbol reads one Huffman symbol from is using this code, updates
// the symbol's frequency, and rebuilds the code if the rebuild frequency
// has been reached.
func (h *huffmanCode) decodeSymbol(is *inputBitstream) int {
	code := uint32(0)
	for l := 1; l <= maxCodewordLen; l++ {
		code = (code << 1) | is.readBits(1)
		count := h.firstSymIdx[l+1] - h.firstSymIdx[l]
		off := code - h.firstCode[l]
		if off < uint32(count) {
			sym := h.symsByLength[h.firstSymIdx[l]+int(off)]
			h.afterDecodeOrEncode(sym)
			return sym
		}
	}
	// Should never happen for a well-formed code; return 0 defensively.
	return 0
}

func (h *huffmanCode) afterDecodeOrEncode(sym int) {
	h.freqs[sym]++
	h.untilRebuild--
	if h.untilRebuild == 0 {
		h.rebuild()
		h.dilute()
	}
}

// encodeSymbol writes sym's codeword to os using this code, then updates
// its frequency and rebuilds the code if needed.
func (h *huffmanCode) encodeSymbol(os *outputBitstream, sym int) {
	os.writeBits(h.codewords[sym], uint(h.lens[sym]))
	h.afterDecodeOrEncode(sym)
}

// ---------------------------------------------------------------------
// Canonical Huffman code construction (wimlib compress_common.c port)
// ---------------------------------------------------------------------

// buildCanonicalHuffmanCode constructs a length-limited canonical Huffman
// code for numSyms symbols (numbered [0, numSyms)) given their
// frequencies, filling in lens[sym] (codeword length, 0 if freq[sym]==0)
// and codewords[sym] (right-justified codeword, undefined if freq[sym]==0).
//
// This is a direct, semantics-preserving port of
// make_canonical_huffman_code()/build_tree()/compute_length_counts()/
// gen_codewords() from wimlib's src/compress_common.c, without the
// bit-packing micro-optimizations of the original C (which packed a
// symbol number into the low bits of each frequency word); those
// optimizations are not needed here since correctness, not raw speed, is
// the goal, but the *order of operations and tie-breaking* is preserved
// exactly, which is what determines the resulting code.
func buildCanonicalHuffmanCode(numSyms, maxLen int, freqs []uint32, lens []uint8, codewords []uint32) {
	for i := range lens {
		lens[i] = 0
	}

	used := make([]symFreq, 0, numSyms)
	for sym := 0; sym < numSyms; sym++ {
		if freqs[sym] != 0 {
			used = append(used, symFreq{sym, freqs[sym]})
		}
	}
	sort.Sort(byFreqAscSymAsc(used))

	numUsed := len(used)

	if numUsed == 0 {
		return
	}

	if numUsed == 1 {
		sym := used[0].sym
		nonzeroIdx := sym
		if sym == 0 {
			nonzeroIdx = 1
		}
		codewords[0] = 0
		lens[0] = 1
		codewords[nonzeroIdx] = 1
		lens[nonzeroIdx] = 1
		return
	}

	// a[i] initially holds the frequency of the i'th node (in ascending
	// order, leaves first). As build_tree() runs, entries are
	// progressively repurposed to hold a parent-node index once that
	// node becomes a child of another node.
	a := make([]uint32, numUsed)
	for i, sf := range used {
		a[i] = sf.freq
	}

	lastIdx := numUsed - 1
	buildTree(a, lastIdx)

	rootIdx := numUsed - 2
	lenCounts := make([]int, maxLen+2)
	computeLengthCounts(a, rootIdx, lenCounts, maxLen)

	genCodewords(used, lenCounts, maxLen, numSyms, lens, codewords)
}

// symFreq pairs a symbol with its frequency for sorting purposes in
// buildCanonicalHuffmanCode/genCodewords.
type symFreq struct {
	sym  int
	freq uint32
}

// byFreqAscSymAsc implements sort.Interface for []symFreq, ordering by
// ascending frequency and, for ties, ascending symbol number. It exists so
// the hot canonical-Huffman path can use sort.Sort (a concrete, inlinable
// comparison) instead of sort.Slice's reflection-based closure calls.
type byFreqAscSymAsc []symFreq

func (s byFreqAscSymAsc) Len() int      { return len(s) }
func (s byFreqAscSymAsc) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byFreqAscSymAsc) Less(i, j int) bool {
	if s[i].freq != s[j].freq {
		return s[i].freq < s[j].freq
	}
	return s[i].sym < s[j].sym
}

// buildTree is a direct port of build_tree(): given ascending frequencies
// in a[0:lastIdx+1], it overwrites a[0:lastIdx] with the stripped-down
// Huffman tree (parent index of each non-leaf node; the root is
// a[lastIdx-1] and needs no parent).
func buildTree(a []uint32, lastIdx int) {
	i, b, e := 0, 0, 0
	for {
		var newFreq uint32
		switch {
		case i+1 <= lastIdx && (b == e || a[i+1] <= a[b]):
			// Two leaves.
			newFreq = a[i] + a[i+1]
			i += 2
		case b+2 <= e && (i > lastIdx || a[b+1] < a[i]):
			// Two non-leaves.
			newFreq = a[b] + a[b+1]
			a[b] = uint32(e)
			a[b+1] = uint32(e)
			b += 2
		default:
			// One leaf, one non-leaf.
			newFreq = a[i] + a[b]
			a[b] = uint32(e)
			i++
			b++
		}
		a[e] = newFreq
		e++
		if e >= lastIdx {
			break
		}
	}
}

// computeLengthCounts is a direct port of compute_length_counts(): given
// the tree produced by buildTree, determine how many codewords must have
// each length (subject to the maxLen limit).
func computeLengthCounts(a []uint32, rootIdx int, lenCounts []int, maxLen int) {
	for l := range lenCounts {
		lenCounts[l] = 0
	}
	lenCounts[1] = 2

	a[rootIdx] = 0 // depth of root

	for node := rootIdx - 1; node >= 0; node-- {
		parent := int(a[node])
		parentDepth := int(a[parent])
		depth := parentDepth + 1
		length := depth

		a[node] = uint32(depth)

		if length >= maxLen {
			length = maxLen
			for lenCounts[length] == 0 {
				length--
			}
		}

		lenCounts[length]--
		lenCounts[length+1] += 2
	}
}

// genCodewords is a direct port of gen_codewords(): assigns codeword
// lengths to symbols (longest lengths to the lowest-frequency symbols
// first, per lenCounts) and then generates canonical codewords.
func genCodewords(used []symFreq, lenCounts []int, maxLen, numSyms int, lens []uint8, codewords []uint32) {
	i := 0
	for l := maxLen; l >= 1; l-- {
		count := lenCounts[l]
		for ; count > 0; count-- {
			lens[used[i].sym] = uint8(l)
			i++
		}
	}

	nextCodeword := make([]uint32, maxLen+1)
	nextCodeword[0] = 0
	if maxLen >= 1 {
		nextCodeword[1] = 0
	}
	for l := 2; l <= maxLen; l++ {
		nextCodeword[l] = (nextCodeword[l-1] + uint32(lenCounts[l-1])) << 1
	}

	for sym := 0; sym < numSyms; sym++ {
		l := lens[sym]
		if l == 0 {
			continue
		}
		codewords[sym] = nextCodeword[l]
		nextCodeword[l]++
	}
}
