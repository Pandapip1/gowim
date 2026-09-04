package xpress

import "container/heap"

// huffman.go builds and consumes the single canonical Huffman code XPRESS
// uses for its 512-symbol match/literal alphabet, and packs/unpacks its
// on-disk representation: 512 codeword lengths as one byte per two symbols
// (a 4-bit nibble each), transmitted before the coded item stream.
//
// The canonical-code construction (turning a set of codeword lengths into
// actual codewords) follows the standard definition used by DEFLATE and
// documented by wimlib's make_canonical_huffman_code: codewords are assigned
// in order of increasing length, and, within a length, in order of
// increasing symbol value. Because that assignment is fully determined by
// the lengths alone, an encoder and a decoder that both apply it to the same
// transmitted lengths always agree on the codewords -- this package does not
// need to reproduce wimlib's specific length-selection heuristics (which
// only affect compression ratio, not decodability; see xpress.go).

// packHuffmanLengths packs 512 4-bit codeword lengths into the 256-byte
// on-disk header (low nibble = even symbol, high nibble = odd symbol).
func packHuffmanLengths(lens [numSymbols]uint8) [huffmanHeaderSize]byte {
	var out [huffmanHeaderSize]byte
	for i := 0; i < huffmanHeaderSize; i++ {
		out[i] = (lens[2*i+1] << 4) | (lens[2*i] & 0xf)
	}
	return out
}

// unpackHuffmanLengths is the inverse of packHuffmanLengths.
func unpackHuffmanLengths(b []byte) [numSymbols]uint8 {
	var lens [numSymbols]uint8
	for i := 0; i < huffmanHeaderSize; i++ {
		lens[2*i] = b[i] & 0xf
		lens[2*i+1] = b[i] >> 4
	}
	return lens
}

// canonicalCodewords assigns a canonical Huffman codeword to every symbol
// with a nonzero length, using the standard length-ordered construction
// (equivalent to RFC 1951 section 3.2.2). Codewords are returned
// right-justified (i.e. as plain integers whose low `len` bits, read MSB
// first, are the codeword), matching how bitWriter.writeBits/bitReader
// expect them.
func canonicalCodewords(lens [numSymbols]uint8) [numSymbols]uint16 {
	var blCount [maxCodewordLen + 1]uint32
	for _, l := range lens {
		if l > 0 {
			blCount[l]++
		}
	}

	var nextCode [maxCodewordLen + 1]uint32
	var code uint32
	for bits := 1; bits <= maxCodewordLen; bits++ {
		code = (code + blCount[bits-1]) << 1
		nextCode[bits] = code
	}

	var codewords [numSymbols]uint16
	for sym := 0; sym < numSymbols; sym++ {
		l := lens[sym]
		if l == 0 {
			continue
		}
		codewords[sym] = uint16(nextCode[l])
		nextCode[l]++
	}
	return codewords
}

// flatLens is the fixed codeword-length table the None preset uses (see
// Options.SkipSearch and None in options.go): literal symbols 0-254 get
// length 8, literal symbol 255 and endOfData get length 9, and every other
// match-header symbol gets length 0 (unused, since a None-preset stream
// never emits a match).
//
// A full 256 codewords of length 8 would exactly saturate the code space
// (256 * 2^-8 == 1 by Kraft's inequality), leaving no room for any other
// codeword -- including endOfData's, which real decoders such as
// Microsoft's WIMGAPI require (see compressDefault's doc in encode.go).
// Trading one literal (255) down to length 9 frees exactly enough budget
// for two length-9 codewords: 255*2^-8 + 2*2^-9 == 1. One of those goes to
// the now length-9 literal 255, the other to endOfData, so a None-preset
// stream still carries a valid end-of-data marker.
//
// Because canonicalCodewords assigns codewords in increasing-length-then-
// increasing-symbol order (see its doc), all 255 length-8 codewords (for
// literals 0-254) are assigned before either length-9 codeword, and the
// assignment among same-length symbols reduces to plain binary counting up
// from 0 in symbol order. So canonicalCodewords(flatLens)[b] == b for every
// b in [0,254] -- byte 255 is the one exception, costing 9 bits instead of
// 8 since it shares its length class with endOfData.
// TestCanonicalCodewordsFlatIsIdentity locks this in.
var flatLens = func() [numSymbols]uint8 {
	var lens [numSymbols]uint8
	for sym := 0; sym < numChars-1; sym++ {
		lens[sym] = 8
	}
	lens[numChars-1] = 9
	lens[endOfData] = 9
	return lens
}()

// flatCodewords is canonicalCodewords(flatLens), precomputed once at
// package init since flatLens never changes.
var flatCodewords = canonicalCodewords(flatLens)

// --- Length construction (encoder side) -------------------------------

// hnode is a node in the Huffman construction tree; sym >= 0 marks a leaf.
type hnode struct {
	weight      uint64
	seq         int // insertion order, used only for deterministic tie-breaking
	sym         int
	left, right *hnode
}

// nodeHeap is a container/heap min-heap over *hnode, ordered by weight, then
// by insertion sequence for determinism.
type nodeHeap []*hnode

func (h nodeHeap) Len() int { return len(h) }
func (h nodeHeap) Less(i, j int) bool {
	if h[i].weight != h[j].weight {
		return h[i].weight < h[j].weight
	}
	return h[i].seq < h[j].seq
}
func (h nodeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x interface{}) { *h = append(*h, x.(*hnode)) }
func (h *nodeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// buildLengths computes a length-limited (<= maxCodewordLen) set of
// codeword lengths for the given symbol frequencies, using a standard
// Huffman tree. In the vanishingly unlikely case that the natural
// (unbounded) Huffman tree would need a codeword longer than
// maxCodewordLen -- which for a 512-symbol alphabet capped at 15 bits
// requires deliberately pathological, near-Fibonacci frequency ratios --
// this falls back to a fixed 9-bit code for every used symbol (2^9 == 512,
// so this is always valid regardless of how many symbols are in use). That
// fallback only affects compression ratio in this pathological case, never
// correctness; see xpress.go for why ratio is not a goal of this encoder.
func buildLengths(freqs *[numSymbols]uint32) [numSymbols]uint8 {
	var lens [numSymbols]uint8

	// All leaf and internal nodes (at most numSymbols leaves and
	// numSymbols-1 internal nodes for a standard binary Huffman tree, so
	// 2*numSymbols-1 total) come from one preallocated backing slice
	// instead of individual heap allocations per node -- the capacity
	// bound means backing never reallocates, so pointers taken into it
	// stay valid for the tree's lifetime.
	backing := make([]hnode, 0, 2*numSymbols-1)
	nodes := make([]*hnode, 0, numSymbols)
	for sym := 0; sym < numSymbols; sym++ {
		if freqs[sym] > 0 {
			backing = append(backing, hnode{weight: uint64(freqs[sym]), seq: len(nodes), sym: sym})
			nodes = append(nodes, &backing[len(backing)-1])
		}
	}
	switch len(nodes) {
	case 0:
		return lens
	case 1:
		lens[nodes[0].sym] = 1
		return lens
	}

	h := make(nodeHeap, 0, len(nodes))
	heap.Init(&h)
	for _, n := range nodes {
		heap.Push(&h, n)
	}
	seq := len(nodes)
	for h.Len() > 1 {
		a := heap.Pop(&h).(*hnode)
		b := heap.Pop(&h).(*hnode)
		backing = append(backing, hnode{weight: a.weight + b.weight, seq: seq, sym: -1, left: a, right: b})
		parent := &backing[len(backing)-1]
		seq++
		heap.Push(&h, parent)
	}
	root := heap.Pop(&h).(*hnode)

	maxDepth := 0
	var assign func(n *hnode, depth int)
	assign = func(n *hnode, depth int) {
		if n.left == nil && n.right == nil {
			lens[n.sym] = uint8(depth)
			if depth > maxDepth {
				maxDepth = depth
			}
			return
		}
		assign(n.left, depth+1)
		assign(n.right, depth+1)
	}
	assign(root, 0)

	if maxDepth > maxCodewordLen {
		for i := range lens {
			lens[i] = 0
		}
		for _, n := range nodes {
			lens[n.sym] = 9
		}
	}
	return lens
}

// --- Decoding ------------------------------------------------------------

// huffmanDecoder is a flat lookup table mapping the next maxCodewordLen bits
// of input to the symbol they decode to and that symbol's codeword length.
// This favors simplicity over the multi-level table wimlib uses for speed;
// correctness only depends on the table faithfully representing the
// canonical code, not on lookup speed.
type huffmanDecoder struct {
	// entry = symbol<<5 | length (length in bits, 1..maxCodewordLen; 0
	// means "no valid codeword has this prefix").
	table [1 << maxCodewordLen]uint16
}

func buildHuffmanDecoder(lens [numSymbols]uint8) *huffmanDecoder {
	codewords := canonicalCodewords(lens)
	d := &huffmanDecoder{}
	for sym := 0; sym < numSymbols; sym++ {
		l := lens[sym]
		if l == 0 {
			continue
		}
		shift := maxCodewordLen - int(l)
		base := int(codewords[sym]) << shift
		count := 1 << shift
		entry := uint16(sym)<<5 | uint16(l)
		for i := 0; i < count; i++ {
			d.table[base+i] = entry
		}
	}
	return d
}

// decode reads one Huffman symbol from r. ok is false if the bit pattern at
// the current position does not correspond to any codeword (malformed
// input).
func (d *huffmanDecoder) decode(r *bitReader) (sym int, ok bool) {
	r.ensureBits(maxCodewordLen)
	entry := d.table[r.peekBits(maxCodewordLen)]
	length := entry & 0x1f
	if length == 0 {
		return 0, false
	}
	r.removeBits(uint32(length))
	return int(entry >> 5), true
}
