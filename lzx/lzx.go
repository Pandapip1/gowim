// Package lzx implements the LZX compression algorithm as used by the WIM
// (Windows Imaging Format) container, over plain in-memory byte buffers.
//
// LZX is a Microsoft LZ77/Huffman compression format originally designed for
// cabinet (.cab) files and later reused, in a restricted form, by WIM. This
// package implements the WIM flavor: a single independently-decodable
// "chunk" per Compress/Decompress call, matching how WIM resources are
// divided into chunks (conventionally 32768 bytes uncompressed) that are
// each compressed independently with no cross-chunk Huffman-table or window
// state carryover. See "Scope" below for the precise boundary between this
// package and a WIM container implementation.
//
// # Sources
//
//   - The historical public specification, "Microsoft LZX Data Compression
//     Format" (1997), describes the CAB-file flavor of LZX. It is riddled
//     with errata and does not describe the WIM flavor at all (no E8
//     filter behavior for WIM, no WIM-style block-size field). It was
//     consulted for historical context only; no bitstream detail in this
//     package is taken from it without cross-checking against wimlib.
//   - The newer interoperability document "[MS-PATCH]: LZX DELTA
//     Compression and Decompression" (2014) documents a related but
//     distinct format (LZX DELTA, with reference-data/"sliding window"
//     extensions) used by Microsoft's binary patcher, not WIM.
//   - The authoritative source for this package is wimlib
//     (https://github.com/ebiggers/wimlib), the real, shipped, independent
//     implementation that actual WIM files (including this workspace's own
//     Windows 11 sample boot.wim, confirmed via `wimlib-imagex info` to use
//     LZX with a 32768-byte chunk size) are compressed/decompressed
//     against. This package was cross-checked against wimlib commit
//     cd5e231c348c255ae5088873b5a66ee0eb96fa07 (2024), specifically:
//     include/wimlib/lzx_constants.h, include/wimlib/lzx_common.h,
//     src/lzx_common.c, src/lzx_decompress.c (whose own header comment
//     explicitly documents the WIM-vs-CAB distinctions), src/lzx_compress.c,
//     and include/wimlib/decompress_common.h (bitstream/Huffman-decode
//     conventions shared with XPRESS).
//
// # WIM-flavor vs. CAB-flavor differences (per src/lzx_decompress.c and
// src/lzx_common.c in wimlib)
//
//   - Window is not "sliding": each chunk uses a fixed window sized to the
//     chunk itself (for WIM's conventional 32768-byte chunk, this is
//     LZX_MIN_WINDOW_ORDER = 15), and chunks do not share window state.
//     CAB-LZX instead maintains one big sliding window and Huffman-table
//     continuity across many blocks in a stream.
//   - The E8 (x86 CALL-instruction) address-translation filter is applied
//     *unconditionally* to every WIM-compressed chunk, using a fixed,
//     magic "file size" parameter of 12000000 regardless of the actual
//     resource size. CAB-LZX instead reserves an explicit bit in its
//     stream header to signal whether the filter is enabled, and uses the
//     real uncompressed size as the filter parameter.
//   - The block-size field uses WIM's compact encoding: 1 bit signaling
//     "default size" (32768), else 16 bits (or 24 bits, as an
//     ebiggers/wimlib extension for window orders >= 16 i.e. buffers over
//     65536 bytes) for an explicit size. The original 1997 CAB-LZX spec
//     used a fixed 24-bit field for every block.
//   - wimlib's own compressor never emits LZX_BLOCKTYPE_UNCOMPRESSED
//     blocks (see the comment on lzx_flush_block in src/lzx_compress.c);
//     this package's decoder still supports decoding one, for robustness
//     against any real-world producer that does, but this package's own
//     encoder does not emit them either.
//
// # Scope
//
// This is a pure, WIM-agnostic codec over raw byte buffers:
//
//	func Compress(data []byte) []byte
//	func Decompress(data []byte, expectedSize int) ([]byte, error)
//
// It deliberately does NOT implement:
//
//   - WIM chunk-table framing, multi-chunk resource splitting, or any
//     other WIM container awareness. Each Compress/Decompress call is one
//     independent, stateless chunk; wiring multiple chunks together with a
//     WIM resource's chunk offset table is a separate, future task at the
//     `wim` package level.
//   - The CAB-LZX "sliding window" / multi-block streaming model, E8
//     filter enable bit, or LZX DELTA's reference-data extensions -- none
//     of these are used by WIM.
//   - Compression-ratio or match-finding optimality. The encoder uses a
//     bounded 3-way-lookahead LZ77 match finder (binary-tree based, bounded
//     search depth, plus a direct check of the repeat-offset LRU queue --
//     see lzx/matcher.go), and additionally tries a bounded multi-state
//     beam DP parse (lzx/optimal.go's findMatchesOptimal, kept only if it
//     encodes smaller than the lookahead parse) -- neither is wimlib's
//     full near-optimal parser, which explores every reachable repeat-
//     offset-queue state without a beam-width cap; see optimal.go's own
//     doc for exactly where this package's DP falls short of that.
//     compress() emits one or more
//     blocks per call: besides the whole-chunk VERBATIM/ALIGNED
//     candidates, it tries a single bounded 2-block midpoint split
//     (encode.go's trySplitChunk) and wimlib's own real, statistics-driven
//     multi-way block-splitting heuristic (lzx/splitstats.go's
//     trySplitChunkStats, ported from wimlib's lzx_should_end_block --
//     see gowim's own TODO.md), keeping whichever candidate encodes
//     smallest -- not a from-scratch general multi-way block-split
//     search of its own. Each block independently VERBATIM or ALIGNED
//     (whichever a same-tokens trial encoding of each comes out smaller
//     as -- see encode.go's encodeBlock/writeBlockInto/
//     buildAlignedTable) -- it never emits an uncompressed block, nor does
//     it use a full iterative bit-cost model across the whole file (beyond
//     the two-pass Huffman-informed re-parse in compress(), which is not
//     the same as a full iterative optimizer). This is a valid, spec-compliant subset (block
//     type is signaled per-block, and wimlib's own compressor already
//     demonstrates that a real decoder must accept an all-verbatim stream)
//     that a compliant decoder, including wimlib, must decode correctly;
//     it is simply not the most compact possible encoding. The E8 filter
//     is applied for parity with WIM's real bitstreams (so this package's
//     own output round-trips through a real WIM/wimlib decoder), but no
//     attempt is made to detect whether the input actually contains x86
//     code -- it is applied unconditionally, exactly as WIM does.
//   - Window orders beyond LZX_MAX_WINDOW_ORDER (21, i.e. buffers larger
//     than 2097152 bytes). WIM itself never asks for more than one
//     32768-byte chunk per call; this cap is generous headroom beyond
//     that and matches wimlib's own documented maximum.
//
// The decoder (Decompress) supports all three LZX block types
// (VERBATIM, ALIGNED, UNCOMPRESSED) since real-world data -- including
// wimlib-produced ALIGNED blocks -- must decode correctly; only the
// encoder (Compress) is scoped down to VERBATIM-only, per above.
package lzx

import "errors"

// Format constants, taken from wimlib's include/wimlib/lzx_constants.h.
const (
	numChars          = 256                           // LZX_NUM_CHARS
	minMatchLen       = 2                             // LZX_MIN_MATCH_LEN
	maxMatchLen       = 257                           // LZX_MAX_MATCH_LEN
	numLens           = maxMatchLen - minMatchLen + 1 // LZX_NUM_LENS
	numPrimaryLens    = 7                             // LZX_NUM_PRIMARY_LENS
	numLenHeaders     = numPrimaryLens + 1            // LZX_NUM_LEN_HEADERS
	minSecondaryLen   = minMatchLen + numPrimaryLens  // LZX_MIN_SECONDARY_LEN
	lenCodeNumSymbols = numLens - numPrimaryLens      // LZX_LENCODE_NUM_SYMBOLS

	blockTypeVerbatim     = 1 // LZX_BLOCKTYPE_VERBATIM
	blockTypeAligned      = 2 // LZX_BLOCKTYPE_ALIGNED
	blockTypeUncompressed = 3 // LZX_BLOCKTYPE_UNCOMPRESSED

	minWindowOrder = 15 // LZX_MIN_WINDOW_ORDER
	maxWindowOrder = 21 // LZX_MAX_WINDOW_ORDER
	minWindowSize  = 1 << minWindowOrder
	maxWindowSize  = 1 << maxWindowOrder // LZX_MAX_WINDOW_SIZE

	maxOffsetSlots = 50 // LZX_MAX_OFFSET_SLOTS

	precodeNumSymbols  = 20 // LZX_PRECODE_NUM_SYMBOLS
	precodeElementSize = 4  // LZX_PRECODE_ELEMENT_SIZE

	numAlignedOffsetBits   = 3                         // LZX_NUM_ALIGNED_OFFSET_BITS
	alignedCodeNumSymbols  = 1 << numAlignedOffsetBits // LZX_ALIGNEDCODE_NUM_SYMBOLS
	alignedCodeElementSize = 3                         // LZX_ALIGNEDCODE_ELEMENT_SIZE
	minAlignedOffsetSlot   = 8                         // LZX_MIN_ALIGNED_OFFSET_SLOT

	maxMainCodewordLen    = 16 // LZX_MAX_MAIN_CODEWORD_LEN
	maxLenCodewordLen     = 16 // LZX_MAX_LEN_CODEWORD_LEN
	maxPrecodeCodewordLen = 15 // LZX_MAX_PRE_CODEWORD_LEN ((1<<4)-1)
	maxAlignedCodewordLen = 7  // LZX_MAX_ALIGNED_CODEWORD_LEN ((1<<3)-1)

	numRecentOffsets = 3 // LZX_NUM_RECENT_OFFSETS

	// wimMagicFilesize is the fixed "file size" parameter WIM always uses
	// for E8 call-instruction preprocessing, regardless of the actual
	// resource/chunk size. See lzx_constants.h: LZX_WIM_MAGIC_FILESIZE.
	wimMagicFilesize = 12000000

	// defaultBlockSize is the block size implied by the WIM block-header
	// "default size" bit. See lzx_constants.h: LZX_DEFAULT_BLOCK_SIZE.
	defaultBlockSize = 32768
)

// lzxOffsetSlotBase maps an offset slot to the first match offset that uses
// that slot. Slots 0-2 map to "fake" offsets < 1 and are only meaningful as
// placeholders for the repeat-offset LRU queue. Copied verbatim from
// wimlib's src/lzx_common.c (lzx_offset_slot_base).
var lzxOffsetSlotBase = [maxOffsetSlots + 1]int32{
	-2, -1, 0, 1, 2,
	4, 6, 10, 14, 22,
	30, 46, 62, 94, 126,
	190, 254, 382, 510, 766,
	1022, 1534, 2046, 3070, 4094,
	6142, 8190, 12286, 16382, 24574,
	32766, 49150, 65534, 98302, 131070,
	196606, 262142, 393214, 524286, 655358,
	786430, 917502, 1048574, 1179646, 1310718,
	1441790, 1572862, 1703934, 1835006, 1966078,
	2097150,
}

// lzxExtraOffsetBits maps an offset slot to the number of extra bits that
// must be read (in a verbatim block) and added to lzxOffsetSlotBase[slot] to
// decode the full match offset. Copied verbatim from wimlib's
// src/lzx_common.c (lzx_extra_offset_bits).
var lzxExtraOffsetBits = [maxOffsetSlots]uint8{
	0, 0, 0, 0, 1,
	1, 2, 2, 3, 3,
	4, 4, 5, 5, 6,
	6, 7, 7, 8, 8,
	9, 9, 10, 10, 11,
	11, 12, 12, 13, 13,
	14, 14, 15, 15, 16,
	16, 17, 17, 17, 17,
	17, 17, 17, 17, 17,
	17, 17, 17, 17, 17,
}

// ErrInvalidData is returned by Decompress when the compressed input is
// malformed (bad block type, out-of-range match offset/length, truncated
// stream shape, etc).
var ErrInvalidData = errors.New("lzx: invalid compressed data")

// windowOrder returns the LZX window order (log2 of the window size) used
// for a buffer of the given uncompressed size, mirroring wimlib's
// lzx_get_window_order(): the smallest order in [minWindowOrder,
// maxWindowOrder] whose window size is >= size. Returns an error if size is
// not positive or exceeds maxWindowSize.
func windowOrder(size int) (int, error) {
	if size <= 0 {
		return 0, errors.New("lzx: size must be positive")
	}
	if size > maxWindowSize {
		return 0, errors.New("lzx: size exceeds maximum supported LZX window (2097152 bytes)")
	}
	order := minWindowOrder
	for (1 << uint(order)) < size {
		order++
	}
	return order, nil
}

// numOffsetSlots returns the number of offset slots used by the main
// Huffman code for the given window order, mirroring wimlib's
// lzx_get_num_main_syms() derivation.
func numOffsetSlots(order int) int {
	windowSize := int32(1) << uint(order)
	maxOffset := windowSize - minMatchLen - 1
	slots := 30
	for maxOffset >= lzxOffsetSlotBase[slots] {
		slots++
	}
	return slots
}

// numMainSyms returns the number of symbols in the main Huffman code for
// the given window order.
func numMainSyms(order int) int {
	return numChars + numOffsetSlots(order)*numLenHeaders
}

// Compress compresses data using the WIM flavor of LZX, treating the entire
// input as a single independent chunk (as documented at the package level,
// this package implements no cross-call state or WIM chunk framing). It
// always succeeds for any input up to maxWindowSize (2097152) bytes; it
// panics if data is larger than that, since that exceeds any WIM chunk size
// in practice and this package does not implement CAB-LZX's sliding window.
//
// The returned data decodes back to an identical copy of data via
// Decompress(result, len(data)).
//
// Compress uses this package's default speed/ratio tuning. Use CompressWith
// to pick a different point on that tradeoff (see Options and its preset
// ladder in options.go); Compress(data) is exactly CompressWith(data,
// Options{}).
func Compress(data []byte) []byte {
	return CompressWith(data, Options{})
}

// CompressWith is Compress with an explicit speed/compression-ratio
// tradeoff. See Options for the individual knobs and Fastest/Fast/
// DefaultOptions/Max for the measured preset ladder most callers should
// use; the zero Options is the default tuning, so CompressWith(data,
// Options{}) and Compress(data) produce identical bytes.
//
// Every preset and every combination of fields produces a valid LZX chunk:
// opts only affects how hard the encoder searches, never the format, so the
// result always decodes back to an identical copy of data via
// Decompress(result, len(data)) -- and through a real independent decoder
// (wimlib) just the same.
func CompressWith(data []byte, opts Options) []byte {
	if len(data) == 0 {
		return []byte{}
	}
	if len(data) > maxWindowSize {
		panic("lzx: Compress: input exceeds maximum supported LZX window (2097152 bytes)")
	}
	return compress(data, opts.resolve())
}

// Decompress decompresses a single LZX chunk produced by Compress (or by a
// real WIM/wimlib encoder for a single chunk's compressed bytes), given the
// exact expected uncompressed size. It returns ErrInvalidData (wrapped) if
// the compressed data is malformed or inconsistent with expectedSize.
func Decompress(data []byte, expectedSize int) (out []byte, err error) {
	if expectedSize < 0 {
		return nil, errors.New("lzx: expectedSize must not be negative")
	}
	if expectedSize == 0 {
		return []byte{}, nil
	}
	// The hand-rolled bit/Huffman decoding below is careful to bounds-check
	// on the paths taken by well-formed data, but defend in depth against
	// any edge case that would otherwise panic on malformed input.
	defer func() {
		if r := recover(); r != nil {
			out = nil
			err = ErrInvalidData
		}
	}()
	return decompress(data, expectedSize)
}
