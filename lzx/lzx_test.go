package lzx

import (
	"bytes"
	"math/rand"
	"os"
	"testing"
)

// TestBuildLengthsSingleUsedSymbolIsComplete guards against a real bug found
// and fixed during gowim/wim's write-side integration testing (2026-07-10):
// when exactly one symbol in an alphabet had nonzero frequency, buildLengths
// used to assign it codeword length 1 and leave every other symbol at length
// 0. That is a valid *prefix* code but an *incomplete* one (Kraft sum 1/2,
// not 1), and real WIM data produced this way was rejected outright by
// wimlib's decoder: wimlib's make_huffman_decode_table
// (src/decompress_common.c) explicitly rejects any incomplete code unless it
// is completely empty (no symbols used at all) -- confirmed by direct calls
// into libwim's wimlib_decompress during development, and reproduced
// end-to-end via wimlib-imagex extract on a hand-assembled WIM.
//
// wimlib's own encoder (src/compress_common.c,
// make_canonical_huffman_code) handles the single-used-symbol case by
// assigning a second, otherwise-unused codeword (symbol 0, or symbol 1 if
// the real symbol is 0) so the code has exactly two length-1 codewords,
// making it complete while staying canonical (the lower-valued symbol still
// gets codeword 0). buildLengths now does the same.
func TestBuildLengthsSingleUsedSymbolIsComplete(t *testing.T) {
	tests := []struct {
		name       string
		usedSymbol int
		n          int
	}{
		{"used symbol nonzero", 5, 16},
		{"used symbol is zero", 0, 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			freqs := make([]uint32, tc.n)
			freqs[tc.usedSymbol] = 100
			lens := buildLengths(freqs, 16)

			numLen1 := 0
			for _, l := range lens {
				if l == 1 {
					numLen1++
				}
			}
			if numLen1 != 2 {
				t.Fatalf("expected exactly 2 length-1 codewords (complete code), got %d; lens=%v", numLen1, lens)
			}
			if lens[tc.usedSymbol] != 1 {
				t.Fatalf("used symbol %d has length %d, want 1", tc.usedSymbol, lens[tc.usedSymbol])
			}
			other := 0
			if tc.usedSymbol == 0 {
				other = 1
			}
			if lens[other] != 1 {
				t.Fatalf("expected dummy symbol %d to get length 1, got %d", other, lens[other])
			}
		})
	}
}

func roundTrip(t *testing.T, name string, data []byte) {
	t.Helper()
	compressed := Compress(data)
	got, err := Decompress(compressed, len(data))
	if err != nil {
		t.Fatalf("%s: Decompress error: %v", name, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s: round-trip mismatch: got %d bytes, want %d bytes", name, len(got), len(data))
	}
}

func TestRoundTripEmpty(t *testing.T) {
	roundTrip(t, "empty", nil)
}

func TestRoundTripSingleByte(t *testing.T) {
	roundTrip(t, "single byte", []byte{0x42})
	roundTrip(t, "single E8 byte", []byte{0xE8})
}

func TestRoundTripRepetitive(t *testing.T) {
	data := bytes.Repeat([]byte("AB"), 20000) // 40000 bytes, spans the 32768 chunk boundary
	roundTrip(t, "repetitive", data)
}

func TestRoundTripAllZeros(t *testing.T) {
	roundTrip(t, "all zeros", make([]byte, 100000))
}

func TestRoundTripRandomIncompressible(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	data := make([]byte, 50000)
	r.Read(data)
	roundTrip(t, "random", data)
}

func TestRoundTripChunkBoundary(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for _, size := range []int{32767, 32768, 32769, 1, 2, 3, 65535, 65536, 65537} {
		data := make([]byte, size)
		r.Read(data)
		roundTrip(t, "boundary", data)
	}
}

// x86CodeLike synthesizes data resembling x86 machine code with a generous
// sprinkling of E8 (CALL rel32) instructions with plausible-looking relative
// displacements, to exercise the E8 address-translation filter.
func x86CodeLike(n int) []byte {
	r := rand.New(rand.NewSource(3))
	data := make([]byte, n)
	i := 0
	for i < n {
		if i+5 <= n && r.Intn(6) == 0 {
			data[i] = 0xE8
			disp := int32(r.Intn(20000) - 10000)
			data[i+1] = byte(disp)
			data[i+2] = byte(disp >> 8)
			data[i+3] = byte(disp >> 16)
			data[i+4] = byte(disp >> 24)
			i += 5
		} else {
			data[i] = byte(r.Intn(256))
			i++
		}
	}
	return data
}

func TestRoundTripX86Like(t *testing.T) {
	roundTrip(t, "x86-like small", x86CodeLike(2000))
	roundTrip(t, "x86-like chunk", x86CodeLike(32768))
}

func TestDecompressEmptyExpected(t *testing.T) {
	got, err := Decompress([]byte{1, 2, 3}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %d bytes", len(got))
	}
}

func TestDecompressInvalidData(t *testing.T) {
	// Garbage data that isn't a valid stream for a large expected size
	// should either error or panic-recover into ErrInvalidData, never
	// panic out of Decompress.
	garbage := bytes.Repeat([]byte{0xFF}, 50)
	_, err := Decompress(garbage, 100000)
	if err == nil {
		t.Fatalf("expected an error decoding garbage data")
	}
}

// realDesktopIniLZXChunk is the exact, real, single-chunk LZX-compressed
// bytes of "/Program Files/desktop.ini" from the sources/boot.wim resource
// of a real Windows 11 23H2 install image (tiny11 23H2 x64), extracted
// directly from the WIM resource's byte range (this file is small enough to
// be a single 32768-byte WIM chunk, so no chunk-table framing is involved).
// realDesktopIniPlaintext is the same file's contents, extracted
// independently via `wimlib-imagex extract` (real, independent ground
// truth). Together these verify this package's decoder against real-world
// data produced by Microsoft's WIMGAPI (the file predates and is unrelated
// to wimlib or this package), not just self-consistency with our own
// encoder.
var realDesktopIniLZXChunk = []byte{
	0x0a, 0x20, 0x00, 0xe1, 0x00, 0x00, 0x02, 0x00, 0x00, 0x30, 0x45, 0x60,
	0xb9, 0x6f, 0xbc, 0x71, 0x8a, 0x3b, 0x50, 0xb1, 0x08, 0xae, 0x86, 0x8b,
	0x2b, 0x24, 0xa1, 0x8e, 0x4b, 0x2b, 0xb1, 0x1f, 0x0d, 0xfb, 0xff, 0xbd,
	0xc9, 0x7f, 0x00, 0xd9, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x08, 0x00,
	0x54, 0xc2, 0xd1, 0x6a, 0x46, 0xa3, 0xe4, 0x45, 0x0b, 0x3f, 0x52, 0xbd,
	0xfe, 0x86, 0x38, 0xfe, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x81, 0x80, 0xff, 0xff, 0xbc, 0xff, 0xae, 0xd2, 0x78, 0x2c, 0x3b, 0x18,
	0xc6, 0x9f, 0xe9, 0x46, 0xa9, 0x72, 0x82, 0xec, 0x63, 0x7e, 0xe1, 0xa7,
	0xd1, 0xe3, 0x74, 0x8c, 0xf8, 0xb0, 0xd8, 0x98, 0x22, 0xd1, 0xe9, 0xbb,
	0xb2, 0xda, 0x88, 0x04, 0xd3, 0x87, 0x3a, 0x19, 0x1c, 0xa8, 0x81, 0x9f,
	0xd1, 0xc4, 0x23, 0x95, 0x14, 0xa2, 0x15, 0x27, 0x52, 0x8f, 0x23, 0x85,
	0x36, 0xca, 0xb4, 0x15, 0x7c, 0xd9, 0xd7, 0xcf, 0x32, 0x26, 0xb6, 0xe5,
	0x52, 0x93, 0xb3, 0x06, 0xbe, 0x71, 0x71, 0xb1, 0x00, 0x60,
}

var realDesktopIniPlaintext = []byte{
	0xff, 0xfe, 0x0d, 0x00, 0x0a, 0x00, 0x5b, 0x00, 0x2e, 0x00, 0x53, 0x00,
	0x68, 0x00, 0x65, 0x00, 0x6c, 0x00, 0x6c, 0x00, 0x43, 0x00, 0x6c, 0x00,
	0x61, 0x00, 0x73, 0x00, 0x73, 0x00, 0x49, 0x00, 0x6e, 0x00, 0x66, 0x00,
	0x6f, 0x00, 0x5d, 0x00, 0x0d, 0x00, 0x0a, 0x00, 0x4c, 0x00, 0x6f, 0x00,
	0x63, 0x00, 0x61, 0x00, 0x6c, 0x00, 0x69, 0x00, 0x7a, 0x00, 0x65, 0x00,
	0x64, 0x00, 0x52, 0x00, 0x65, 0x00, 0x73, 0x00, 0x6f, 0x00, 0x75, 0x00,
	0x72, 0x00, 0x63, 0x00, 0x65, 0x00, 0x4e, 0x00, 0x61, 0x00, 0x6d, 0x00,
	0x65, 0x00, 0x3d, 0x00, 0x40, 0x00, 0x25, 0x00, 0x53, 0x00, 0x79, 0x00,
	0x73, 0x00, 0x74, 0x00, 0x65, 0x00, 0x6d, 0x00, 0x52, 0x00, 0x6f, 0x00,
	0x6f, 0x00, 0x74, 0x00, 0x25, 0x00, 0x5c, 0x00, 0x73, 0x00, 0x79, 0x00,
	0x73, 0x00, 0x74, 0x00, 0x65, 0x00, 0x6d, 0x00, 0x33, 0x00, 0x32, 0x00,
	0x5c, 0x00, 0x73, 0x00, 0x68, 0x00, 0x65, 0x00, 0x6c, 0x00, 0x6c, 0x00,
	0x33, 0x00, 0x32, 0x00, 0x2e, 0x00, 0x64, 0x00, 0x6c, 0x00, 0x6c, 0x00,
	0x2c, 0x00, 0x2d, 0x00, 0x32, 0x00, 0x31, 0x00, 0x37, 0x00, 0x38, 0x00,
	0x31, 0x00, 0x0d, 0x00, 0x0a, 0x00,
}

func TestDecompressRealWIMGroundTruth(t *testing.T) {
	got, err := Decompress(realDesktopIniLZXChunk, len(realDesktopIniPlaintext))
	if err != nil {
		t.Fatalf("Decompress error: %v", err)
	}
	if !bytes.Equal(got, realDesktopIniPlaintext) {
		t.Fatalf("real-data decode mismatch:\n got  %q\n want %q", got, realDesktopIniPlaintext)
	}
}

// TestFindMatchesUsesRepeatOffsetQueue guards the repeat-offset (LRU queue)
// support added to the matcher/encoder (2026-08-18, see gowim's own
// TODO.md): a real, ground-truthed comparison against wimlib's own LZX
// compressor found gowim's encoder producing measurably larger output on
// real data because it never reused the LZX repeat-offset queue, paying
// full offset-encoding cost on every match even when immediately repeating
// the same distance. This constructs data with a distant (large offset,
// i.e. outside the always-free slots 0-3) repeated pattern and checks that
// matches after the first reuse a repeat-offset queue slot.
func TestFindMatchesUsesRepeatOffsetQueue(t *testing.T) {
	pattern := make([]byte, 4000)
	for i := range pattern {
		pattern[i] = byte(i * 7 % 251)
	}
	data := append(append([]byte{}, pattern...), pattern...)
	data = append(data, pattern...)

	toks := findMatches(data, costModel{})
	sawFreshLargeOffset := false
	sawRepeat := false
	for _, tok := range toks {
		if !tok.isMatch {
			continue
		}
		if tok.repeat < 0 && tok.offset >= len(pattern) {
			sawFreshLargeOffset = true
		}
		if tok.repeat >= 0 {
			sawRepeat = true
		}
	}
	if !sawFreshLargeOffset {
		t.Fatalf("expected at least one fresh match at the pattern's repeat distance")
	}
	if !sawRepeat {
		t.Fatalf("expected the repeated pattern to produce repeat-offset matches")
	}
}

// TestRoundTripRepeatOffsetPattern exercises encode/decode correctness for
// data that should trigger heavy repeat-offset queue use (all three queue
// slots), including offsets large enough to require extra offset bits.
func TestRoundTripRepeatOffsetPattern(t *testing.T) {
	a := bytes.Repeat([]byte{0x11, 0x22, 0x33, 0x44, 0x55}, 500) // distinct offset A
	b := bytes.Repeat([]byte{0xAA, 0xBB, 0xCC, 0xDD}, 700)       // distinct offset B
	var data []byte
	// Interleave so the matcher must juggle multiple recent offsets (A, B,
	// A again) rather than just reusing one.
	for i := 0; i < 6; i++ {
		data = append(data, a...)
		data = append(data, b...)
	}
	roundTrip(t, "repeat-offset interleaved", data)
}

// TestCompressAllZerosMatchesWimlibSize guards the precode run-length
// (symbols 17/18) support added to writeCodewordLens (2026-08-18, see
// gowim's own TODO.md): before this fix, an all-zero chunk sent every one
// of its ~496 unused main-alphabet symbols' codeword lengths individually,
// producing 156 compressed bytes for a case where real wimlib -- which
// collapses long runs of unused symbols into a couple of precode run-length
// symbols -- produces exactly 78. This is a strict, exact-value regression
// guard (not a range check) precisely because 78 is real, ground-truthed
// wimlib output for this exact input, not an estimate.
func TestCompressAllZerosMatchesWimlibSize(t *testing.T) {
	data := make([]byte, 32768)
	out := Compress(data)
	const wimlibSize = 78
	if len(out) != wimlibSize {
		t.Fatalf("got %d compressed bytes for an all-zero chunk, want exactly %d (wimlib's real output)", len(out), wimlibSize)
	}
}

// TestFindMatchesNeverSelfMatchesAtPositionZero guards a real bug found
// while adding lazy (one-step lookahead) matching (2026-08-18): inserting a
// position's own hash entry *before* searching for its match let position 0
// match against itself at offset 0 (an immediately invalid match, since a
// match offset must be >= 1) whenever findMatches was restructured to
// insert eagerly to support the lazy peek at i+1. Any repeated short
// pattern reproduces this at position 0.
func TestFindMatchesNeverSelfMatchesAtPositionZero(t *testing.T) {
	data := bytes.Repeat([]byte("AB"), 50)
	toks := findMatches(data, costModel{})
	if len(toks) == 0 {
		t.Fatal("expected at least one token")
	}
	if toks[0].isMatch && toks[0].offset == 0 {
		t.Fatalf("first token is an invalid offset-0 self-match: %+v", toks[0])
	}
	for _, tok := range toks {
		if tok.isMatch && tok.offset <= 0 {
			t.Fatalf("found match with non-positive offset: %+v", tok)
		}
	}
}

// TestCostModelPrefersCheaperOffset guards the cost-aware match selection
// added to chooseMatch (2026-08-18, see gowim's own TODO.md): candidates
// are now ranked by an estimated bit value (length saved minus offset/
// symbol cost) rather than raw match length, so a shorter match at a much
// cheaper offset should be preferred over a longer match at a very
// expensive (many-extra-bits) offset when the value works out that way.
func TestCostModelPrefersCheaperOffset(t *testing.T) {
	m := costModel{}
	// Same match length, different offset slots: value must strictly
	// decrease as the slot's extra-bit cost increases, isolating the
	// offset-cost effect from length.
	cheapSlot := 4   // lzxExtraOffsetBits[4] == 1
	costlySlot := 34 // lzxExtraOffsetBits[34] == 16
	const length = 10
	cheapValue := m.matchValue(cheapSlot, length, int(lzxExtraOffsetBits[cheapSlot]), length*flatLiteralBits)
	costlyValue := m.matchValue(costlySlot, length, int(lzxExtraOffsetBits[costlySlot]), length*flatLiteralBits)
	if cheapValue <= costlyValue {
		t.Fatalf("expected cheap-offset match to have higher value at equal length: cheap=%d costly=%d", cheapValue, costlyValue)
	}

	// A length-10 match at a cheap offset should also beat a slightly
	// longer (length-11) match at the costly offset, since the extra-bit
	// cost difference (15 bits) dwarfs one byte's worth of length (8 bits).
	costlyLonger := m.matchValue(costlySlot, length+1, int(lzxExtraOffsetBits[costlySlot]), (length+1)*flatLiteralBits)
	if cheapValue <= costlyLonger {
		t.Fatalf("expected cheap-offset match to beat a slightly longer costly-offset match: cheap=%d costlyLonger=%d", cheapValue, costlyLonger)
	}
}

// TestTwoPassRefinementRunsWithoutPanicking guards the two-pass encode
// (2026-08-18): pass 2's costModel is built from pass 1's Huffman lengths,
// which must be indexable by every main/length symbol pass 2's parse can
// produce (in particular, an all-zero Huffman freq table would leave a
// short mainLens slice if buildLengths ever changed its output size --
// this pins the assumption that mainLens1/lenLens1 are always exactly
// numMainSyms(order)/lenCodeNumSymbols long).
func TestTwoPassRefinementRunsWithoutPanicking(t *testing.T) {
	roundTrip(t, "two-pass small", []byte("hello, world! hello, world! hello, world!"))
	roundTrip(t, "two-pass empty-ish", []byte{0})
	roundTrip(t, "two-pass single-symbol", bytes.Repeat([]byte{0x7A}, 5000))
}

// TestAlignedBlockCanBeSmaller guards the ALIGNED-block trial added to
// compress() (2026-08-18, see gowim's own TODO.md): the encoder now
// encodes each chunk both as VERBATIM and ALIGNED and keeps whichever is
// smaller. This builds a synthetic token stream of fresh matches at large
// offsets (slot >= minAlignedOffsetSlot) whose low 3 extra-offset bits are
// heavily skewed toward one value -- exactly the case a skewed 8-symbol
// aligned Huffman code should beat 3 raw bits per match on -- and checks
// encodeBlock directly produces a smaller ALIGNED encoding for it.
func TestAlignedBlockCanBeSmaller(t *testing.T) {
	order := 15
	nMainSyms := numMainSyms(order)
	var toks []token
	slot := 20 // well above minAlignedOffsetSlot(8)
	base := uint32(lzxOffsetSlotBase[slot])
	for i := 0; i < 500; i++ {
		extra := uint32(0)
		if i%20 == 0 {
			extra = 5 // rare outlier
		}
		toks = append(toks, token{isMatch: true, offset: int(base + extra), length: 10, repeat: -1})
	}

	mainLens, lenLens := buildTables(toks, nMainSyms)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

	data := make([]byte, defaultBlockSize) // dummy; only its length is used for the block header
	verbatim := encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, nil, nil)
	alignedLens, alignedCodes := buildAlignedTable(toks)
	aligned := encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes)

	if len(aligned) >= len(verbatim) {
		t.Fatalf("expected ALIGNED encoding to be smaller for skewed low-offset-bits data: verbatim=%d aligned=%d", len(verbatim), len(aligned))
	}
}

// TestFindMatchesBSTFindsGlobalBestWithinSmallBuffer guards the binary-tree
// match finder added to findMatches (2026-08-18, replacing an earlier
// hash-chain finder -- see gowim's own TODO.md): every reported match must
// be a *real* match (the claimed offset/length must actually agree with the
// data), and for a buffer small enough that maxChainLen (96) can never be
// exceeded, the BST must find the true longest available match at every
// position, not just "a" match -- a wrong left/right branch invariant in
// the tree would silently miss real matches without ever producing an
// invalid one, so this checks both properties against a brute-force
// reference.
func TestFindMatchesBSTFindsGlobalBestWithinSmallBuffer(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	n := 500 // well under maxChainLen(96)*small alphabet collision fan-out
	alphabet := 6
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(r.Intn(alphabet))
	}

	toks := findMatches(data, costModel{})

	bruteBestLen := func(pos int) int {
		best := 0
		limit := n - pos
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		for c := 0; c < pos; c++ {
			cLimit := n - c
			if cLimit > limit {
				cLimit = limit
			}
			l := 0
			for l < cLimit && data[c+l] == data[pos+l] {
				l++
			}
			if l > best {
				best = l
			}
		}
		return best
	}

	pos := 0
	for _, tok := range toks {
		if !tok.isMatch {
			pos++
			continue
		}
		// Real-match check: the claimed offset/length must actually agree
		// with the data (this alone would catch a branch-direction bug
		// that fabricates a nonexistent match).
		src := pos - tok.offset
		if src < 0 || pos+tok.length > n {
			t.Fatalf("at pos %d: match offset/length out of bounds: off=%d len=%d", pos, tok.offset, tok.length)
		}
		for k := 0; k < tok.length; k++ {
			if data[src+k] != data[pos+k] {
				t.Fatalf("at pos %d: claimed match is not real at byte %d (off=%d len=%d)", pos, k, tok.offset, tok.length)
			}
		}
		// Global-best check: within this small buffer, the BST's search
		// depth budget can never be exhausted before finding the true
		// longest match, so the chosen match's length should equal the
		// brute-force best (a repeat-offset match may legitimately be
		// slightly shorter than the brute-force fresh-offset best, since
		// the cost model can prefer it -- only check fresh matches here).
		if tok.repeat < 0 {
			want := bruteBestLen(pos)
			if tok.length > want {
				t.Fatalf("at pos %d: reported length %d exceeds brute-force best %d (impossible)", pos, tok.length, want)
			}
		}
		pos += tok.length
	}
	if pos != n {
		t.Fatalf("tokens don't cover the whole buffer: covered %d of %d", pos, n)
	}
}

// TestCodewordLenTokensUsesSymbol19 guards the precode symbol 19 support
// in codewordLenTokens (2026-08-18, see gowim's own TODO.md): a run of
// 4-5 consecutive entries that resolve to the same *nonzero* codeword
// length should collapse into one symbol 19 token (plus a delta value
// computed from the run's first position) instead of 4-5 individual
// symbols. Grouped by the actual new length (lens[i]) being equal, not by
// delta equality -- see codewordLenTokens' own doc for why that
// distinction matters (a real bug once grouped by delta instead).
func TestCodewordLenTokensUsesSymbol19(t *testing.T) {
	lens := make([]byte, 100)
	prevLens := make([]byte, 100)
	for i := 10; i < 15; i++ {
		lens[i] = 12 // an arbitrary nonzero length, repeated
	}
	// prevLens[10..14] stay 0, so delta(10) = (0 - 12) mod 17 = 5.
	toks := codewordLenTokens(lens, prevLens)
	found19 := false
	for _, tok := range toks {
		if tok.presym == 19 {
			found19 = true
			if tok.runLen != 5 || tok.sym2 != 5 {
				t.Fatalf("bad symbol19 token: %+v", tok)
			}
		}
	}
	if !found19 {
		t.Fatalf("expected a symbol19 token, tokens=%+v", toks)
	}
}

// TestRoundTripExercisesSymbol19Indirectly checks that data likely to
// produce runs of equal nonzero main-tree codeword lengths still round-
// trips correctly through the real bit writer/reader (an indirect but
// real exercise of symbol 19's actual encode/decode path, not just token
// generation in isolation).
func TestRoundTripExercisesSymbol19Indirectly(t *testing.T) {
	data := make([]byte, 32768)
	for i := range data {
		data[i] = byte(i % 20)
	}
	roundTrip(t, "symbol19-ish", data)
}

// TestSplitChunkRoundTrips guards the 2-block chunk-splitting trial added
// to compress() (2026-08-18, see gowim's own TODO.md): data with two very
// different halves (repetitive text vs high-entropy random bytes) must
// still round-trip correctly through the real bit writer/reader, whether
// or not the split ends up smaller than the single-block encoding.
func TestSplitChunkRoundTrips(t *testing.T) {
	r := rand.New(rand.NewSource(55))
	first := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 300)
	second := make([]byte, len(first))
	r.Read(second)
	data := append(append([]byte{}, first...), second...)
	roundTrip(t, "split-chunk mixed halves", data)
}

// TestTrySplitChunkProducesValidSplit guards trySplitChunk directly: the
// split point must never fall inside a token (matches may not cross a
// block boundary), both halves must be non-empty, and the resulting
// bitstream must decode back to the original data exactly like the
// single-block path.
func TestTrySplitChunkProducesValidSplit(t *testing.T) {
	r := rand.New(rand.NewSource(55))
	first := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 300)
	second := make([]byte, len(first))
	r.Read(second)
	data := append(append([]byte{}, first...), second...)

	order, err := windowOrder(len(data))
	if err != nil {
		t.Fatalf("windowOrder: %v", err)
	}
	nMainSyms := numMainSyms(order)

	pre := make([]byte, len(data))
	copy(pre, data)
	lzxPreprocess(pre)

	toks1 := findMatches(pre, costModel{})
	mainLens1, lenLens1 := buildTables(toks1, nMainSyms)
	toks := findMatches(pre, costModel{mainLens: mainLens1, lenLens: lenLens1})

	split := trySplitChunk(pre, order, toks, nMainSyms)
	if split == nil {
		t.Fatal("expected trySplitChunk to produce a split for this data")
	}

	got, err := decompress(split, len(data))
	if err != nil {
		t.Fatalf("decompress error: %v", err)
	}
	if !bytes.Equal(got, pre) {
		t.Fatalf("split-chunk decode mismatch")
	}
}

// findMatchesOptimalRoundTrip verifies findMatchesOptimal's tokens are all
// real matches with full coverage, then round-trips them through the real
// encoder/decoder (via the public Compress/Decompress path is NOT used
// here since that exercises the bounded-lookahead parse too -- this
// isolates findMatchesOptimal specifically by building its own Huffman
// tables and calling encodeBlock/Decompress directly).
func findMatchesOptimalRoundTrip(t *testing.T, name string, orig []byte) {
	t.Helper()
	pre := append([]byte{}, orig...)
	lzxPreprocess(pre)

	toks := findMatchesOptimal(pre, costModel{})

	pos := 0
	for ti, tok := range toks {
		if !tok.isMatch {
			pos++
			continue
		}
		src := pos - tok.offset
		if src < 0 || pos+tok.length > len(pre) {
			t.Fatalf("%s: tok[%d] pos=%d out of bounds off=%d len=%d", name, ti, pos, tok.offset, tok.length)
		}
		for k := 0; k < tok.length; k++ {
			if pre[src+k] != pre[pos+k] {
				t.Fatalf("%s: tok[%d] pos=%d not a real match at byte %d", name, ti, pos, k)
			}
		}
		pos += tok.length
	}
	if pos != len(pre) {
		t.Fatalf("%s: coverage mismatch got=%d want=%d", name, pos, len(pre))
	}

	order, _ := windowOrder(len(pre))
	nMainSyms := numMainSyms(order)
	mainLens, lenLens := buildTables(toks, nMainSyms)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)
	out := encodeBlock(pre, order, toks, mainLens, lenLens, mainCodes, lenCodes, nil, nil)

	got, err := Decompress(out, len(orig))
	if err != nil {
		t.Fatalf("%s: decompress error: %v", name, err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("%s: round-trip mismatch", name)
	}
}

// TestFindMatchesOptimalRoundTrips guards findMatchesOptimal (the bounded
// single-queue-trajectory DP parse added 2026-08-18, see optimal.go and
// gowim's own TODO.md) directly against a handful of representative
// inputs. A real bug was found and fixed while adding this (not in
// findMatchesOptimal itself, but in an early ad hoc test harness that
// compared decoded output against the *preprocessed* buffer instead of
// the original pre-E8-filter data -- decompress() always reverses the E8
// filter internally, so that comparison was structurally wrong regardless
// of encoder correctness; the lesson generalized into this helper always
// comparing against the true original).
func TestFindMatchesOptimalRoundTrips(t *testing.T) {
	findMatchesOptimalRoundTrip(t, "all zeros", make([]byte, 32768))
	findMatchesOptimalRoundTrip(t, "repetitive", bytes.Repeat([]byte("AB"), 5000))

	r := rand.New(rand.NewSource(7))
	rnd := make([]byte, 20000)
	r.Read(rnd)
	findMatchesOptimalRoundTrip(t, "random", rnd)

	pattern := []byte("the quick brown fox jumps over the lazy dog")
	patterned := make([]byte, 20000)
	for i := range patterned {
		patterned[i] = pattern[i%len(pattern)]
	}
	findMatchesOptimalRoundTrip(t, "patterned", patterned)
}

// TestFindMatchesOptimalStress runs findMatchesOptimalRoundTrip across many
// random/patterned/mixed inputs of varying sizes -- the same style of
// stress coverage the other parser passes (repeat-offset queue,
// binary-tree finder, bounded lookahead) each got when added.
func TestFindMatchesOptimalStress(t *testing.T) {
	r := rand.New(rand.NewSource(999))
	for trial := 0; trial < 200; trial++ {
		n := r.Intn(4000) + 1
		data := make([]byte, n)
		switch trial % 4 {
		case 0:
			r.Read(data)
		case 1:
			for i := range data {
				data[i] = byte(r.Intn(4))
			}
		case 2:
			pattern := make([]byte, r.Intn(50)+1)
			r.Read(pattern)
			for i := range data {
				data[i] = pattern[i%len(pattern)]
			}
		case 3:
			for i := range data {
				if r.Intn(10) == 0 {
					data[i] = byte(r.Intn(256))
				}
			}
		}
		findMatchesOptimalRoundTrip(t, "stress", data)
	}
}

func TestCompressExceedsMaxWindow(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for oversized input")
		}
	}()
	Compress(make([]byte, maxWindowSize+1))
}

// pseudoASCIIText generates n bytes of pseudo-random lowercase-letter-and-
// space text: unlike a repeated phrase (which the match finder collapses
// into a few very long matches, producing too few "observations" -- see
// splitstats.go -- to exercise the block-split heuristic within a
// reasonable buffer size), this has little enough redundancy that most
// bytes become individual literal observations, closer to real English
// text's own token density.
// TestGreedyHash2RoundTrips round-trips a real-world chunk that originally
// exposed a queue-perturbation corruption bug in this package's first,
// now-removed hash2 implementation (an ex-post token-splice pass,
// hash2greedy.go, replaced 2026-08-18 by native length-2 candidates in
// findMatches/findMatchesOptimal themselves -- see matcher.go's
// hash2Candidate/buildHash2PrevOcc and gowim's own TODO.md). That specific
// bug class (a spliced-in match perturbing the repeat-offset queue for
// later tokens chosen under a stale trajectory) cannot recur now that
// hash2 candidates are evaluated inline by the same parse that already
// tracks queue state correctly for every token -- but this real chunk
// remains a useful general round-trip regression fixture regardless.
//
// testdata/hash2_greedy_chunk1.bin: byte offset 6455296 (chunk 197 of 398)
// of a real 12.4MB ntoskrnl.exe extracted from a real boot.wim during this
// package's wimlib-comparison investigation (see gowim's own TODO.md).
func TestGreedyHash2RoundTrips(t *testing.T) {
	data, err := os.ReadFile("testdata/hash2_greedy_chunk1.bin")
	if err != nil {
		t.Fatal(err)
	}
	out := compress(data)
	got, err := decompress(out, len(data))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch")
	}
}

// TestHash2BoundaryOffsetDoesNotCrash guards a real crash bug, found by an
// independent review agent's fuzz/integration run (2026-08-18): a length-2
// hash2 candidate's offset can reach windowSize-2 (a match at the very end
// of the window referencing the window's first two bytes), but
// numOffsetSlots/numMainSyms (lzx.go) size the main-symbol alphabet
// assuming the smallest match ever encoded is length minMatch=3 (the
// normal length>=3 fresh-match finder's own floor), whose largest possible
// offset is only windowSize-3 -- one slot short of what a length-2
// candidate can reach. On an exactly-power-of-two-sized chunk (the
// routine case for 32768-byte WIM chunks), this produced a genuine
// out-of-range index into the main Huffman-length table.
//
// This is not actually an encoder oversight to work around: confirmed
// directly from wimlib's real lzx_get_num_main_syms (src/lzx_common.c)
// that the LZX format itself explicitly disallows this exact case (a
// length-2 match at the window's maximum possible offset), specifically
// so the offset-slot table can be one slot smaller. Fixed (originally in
// the now-removed findHash2Candidates, now in matcher.go's
// hash2Candidate, used natively by both findMatches and
// findMatchesOptimal) by rejecting any candidate whose computed main
// symbol doesn't fit within nMainSyms.
func TestHash2BoundaryOffsetDoesNotCrash(t *testing.T) {
	n := 32768
	r := rand.New(rand.NewSource(1))
	data := make([]byte, n)
	r.Read(data)
	// Force the last 2 bytes to equal the first 2 bytes: a length-2
	// candidate at the maximum possible offset (n-2), landing one
	// main-symbol slot past the table's bounds before the fix.
	data[n-2] = data[0]
	data[n-1] = data[1]

	out := compress(data)
	got, err := decompress(out, len(data))
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch")
	}
}

func pseudoASCIIText(n int, seed int64) []byte {
	const letters = "abcdefghijklmnopqrstuvwxyz "
	r := rand.New(rand.NewSource(seed))
	data := make([]byte, n)
	for i := range data {
		data[i] = letters[r.Intn(len(letters))]
	}
	return data
}

// TestLzxBlockSplitPointsFindsRealShift guards lzxBlockSplitPoints
// (splitstats.go, wimlib's real statistics-driven block-splitting
// heuristic, ported after reading wimlib's own source -- see gowim's own
// TODO.md): a chunk whose content shifts sharply partway through (from
// pseudo-ASCII text to random bytes -- a real "few matches, ASCII-only
// literals" to "no matches, full-byte-range literals" shift, exactly what
// wimlib's heuristic is designed to notice) should produce at least one
// split point, and every split point must respect the minStatsBlockSize
// gap from both the start and end of the chunk.
func TestLzxBlockSplitPointsFindsRealShift(t *testing.T) {
	r := rand.New(rand.NewSource(77))
	first := pseudoASCIIText(20000, 77)
	second := make([]byte, 20000)
	r.Read(second)
	data := append(append([]byte{}, first...), second...)

	pre := make([]byte, len(data))
	copy(pre, data)
	lzxPreprocess(pre)

	toks1 := findMatches(pre, costModel{})
	splits := lzxBlockSplitPoints(toks1, len(pre))
	if len(splits) == 0 {
		t.Fatal("expected at least one split point for a sharp content shift")
	}
	for _, s := range splits {
		if s < minStatsBlockSize || len(pre)-s < minStatsBlockSize {
			t.Fatalf("split point %d violates minStatsBlockSize gap (len=%d)", s, len(pre))
		}
	}
}

// TestTrySplitChunkStatsProducesValidSplit guards trySplitChunkStats
// directly: split points must never fall inside a token, every segment
// must be non-empty, and the resulting bitstream must decode back to the
// original data exactly.
func TestTrySplitChunkStatsProducesValidSplit(t *testing.T) {
	r := rand.New(rand.NewSource(77))
	first := pseudoASCIIText(20000, 77)
	second := make([]byte, 20000)
	r.Read(second)
	data := append(append([]byte{}, first...), second...)

	order, err := windowOrder(len(data))
	if err != nil {
		t.Fatalf("windowOrder: %v", err)
	}
	nMainSyms := numMainSyms(order)

	pre := make([]byte, len(data))
	copy(pre, data)
	lzxPreprocess(pre)

	toks1 := findMatches(pre, costModel{})
	mainLens1, lenLens1 := buildTables(toks1, nMainSyms)
	toks := findMatches(pre, costModel{mainLens: mainLens1, lenLens: lenLens1})

	split := trySplitChunkStats(pre, order, nMainSyms, toks)
	if split == nil {
		t.Fatal("expected trySplitChunkStats to produce a split for this data")
	}

	got, err := decompress(split, len(data))
	if err != nil {
		t.Fatalf("decompress error: %v", err)
	}
	if !bytes.Equal(got, pre) {
		t.Fatal("trySplitChunkStats round-trip mismatch")
	}
}

// TestTrySplitChunkStatsRoundTripsThroughCompress guards the full,
// wired-in Compress()/Decompress() path when trySplitChunkStats' output
// is the smallest candidate, using the same sharp-content-shift data as
// above (post-E8-filter data, matching Compress's own preprocessing).
func TestTrySplitChunkStatsRoundTripsThroughCompress(t *testing.T) {
	r := rand.New(rand.NewSource(77))
	first := pseudoASCIIText(20000, 77)
	second := make([]byte, 20000)
	r.Read(second)
	data := append(append([]byte{}, first...), second...)
	roundTrip(t, "split-chunk-stats sharp content shift", data)
}

// TestCodewordLenTokensGroupsByLengthNotDelta directly guards the real
// bug found and fixed 2026-08-18 (see gowim's own TODO.md): codewordLenTokens
// must group precode runs by whether the ACTUAL NEW codeword length is
// equal (matching wimlib's own lzx_compute_precode_items), not by whether
// the DELTA against prevLens happens to be equal. This test constructs
// lens that are all equal (and nonzero) but prevLens that are NOT
// uniform, so every position's individual delta differs -- a case where
// the two groupings genuinely diverge, unlike an all-zero-prevLens
// baseline where they coincide. Verifies both that codewordLenTokens
// still collapses this into a single symbol-19 run, and that the full
// write/read round trip recovers the original lens exactly.
func TestCodewordLenTokensGroupsByLengthNotDelta(t *testing.T) {
	lens := []byte{8, 8, 8, 8}
	prevLens := []byte{8, 3, 15, 0} // deliberately non-uniform, so per-position deltas (0, 12, 7, 9) are all different

	toks := codewordLenTokens(lens, prevLens)
	found19 := false
	for _, tok := range toks {
		if tok.presym == 19 {
			found19 = true
			if tok.runLen != 4 {
				t.Fatalf("expected symbol19 runLen 4, got %+v", tok)
			}
		}
	}
	if !found19 {
		t.Fatalf("expected lens=%v (all equal, nonzero) to collapse into a symbol19 token despite non-uniform prevLens=%v; tokens=%+v", lens, prevLens, toks)
	}

	w := newBitWriter()
	writeCodewordLens(w, lens, prevLens)
	buf := w.flush()

	rd := newBitReader(buf)
	got := make([]byte, len(lens))
	copy(got, prevLens) // reader is primed with the previous block's real lengths, same as decompress() does
	if err := readCodewordLens(rd, got); err != nil {
		t.Fatalf("readCodewordLens: %v", err)
	}
	if !bytes.Equal(got, lens) {
		t.Fatalf("round trip mismatch: got %v, want %v", got, lens)
	}
}
