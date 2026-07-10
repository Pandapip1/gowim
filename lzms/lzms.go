// Package lzms implements the LZMS compression format used by the
// Windows/WIM ecosystem, introduced in Windows 8 and used for WIM "solid"
// resources and .esd files.
//
// # Sources
//
// LZMS has never been documented by Microsoft. Unlike XPRESS (MS-XCA) or LZX
// (which has an older public CAB-format specification), the only reliable
// account of the wire format is Eric Biggers' wimlib project, which
// reverse-engineered it from real compressed data and Windows's own
// COMPRESS_ALGORITHM_LZMS behavior. wimlib does not ship a standalone
// "lzms-compression-format.txt" document analogous to its Huffman-code
// helper comments in other formats; the closest thing to a specification is
// the extensive block comment at the top of src/lzms_decompress.c, which
// this package treats as authoritative, cross-checked directly against the
// implementations in:
//
//   - src/lzms_decompress.c   (range coder + bitstream + item decoding)
//   - src/lzms_compress.c     (range/bitstream encoder primitives, item
//     encoding order, adaptive-state / LRU-queue update rules)
//   - src/lzms_common.c       (probability model, offset/length slot
//     tables, symbol frequency rules, x86 translation filter)
//   - include/wimlib/lzms_constants.h and lzms_common.h
//   - src/compress_common.c   (make_canonical_huffman_code: the exact
//     Huffman-tree-construction tie-break rule required for bit-for-bit
//     compatibility with wimlib/Microsoft-produced streams)
//
// Cloned from https://github.com/ebiggers/wimlib at commit
// cd5e231c348c255ae5088873b5a66ee0eb96fa07 (2026-01-29). All algorithmic
// decisions in this package (range coder renormalization, the 6-bit
// probability model with a 64-bit recent-bits shift register, Huffman
// rebuild frequencies, the LZ/delta repeat-offset LRU queues with their
// one-item-delayed front-insertion quirk, and the x86 call/jump address
// translation filter) were taken directly from that source, not from
// general recollection of LZMA or other Microsoft compression formats
// (LZMS is a distinct bitstream from LZMA/7-Zip despite superficial
// similarities such as range coding and adaptive probabilities).
//
// # Scope
//
// This package implements only "raw" single-block LZMS compression and
// decompression, matching the behavior of Decompress()/Compress() in the
// Windows 8 compression API when used with COMPRESS_ALGORITHM_LZMS |
// COMPRESS_RAW. It deliberately does NOT implement:
//
//   - Any WIM chunk-table or "solid resource" framing. WIM splits LZMS data
//     into chunks (conventionally 131072 bytes uncompressed) and, for solid
//     resources, packs multiple file blobs into one compressed stream; both
//     of these are container-level concerns that belong in the wim package
//     (see wim.BlobTable's SolidResourceRun), not here. Compress/Decompress
//     in this package operate on exactly one already-delimited buffer.
//   - Any cross-call persistence of adaptive state. LZMS's range coder and
//     probability/Huffman models are inherently stateful across an entire
//     compressed buffer, but that state lives only within a single
//     Compress or Decompress call and is discarded afterward.
//
// # Encoder limitations
//
// The decoder in this package is a complete, faithful implementation of
// the format: it decodes literals, LZ matches (explicit and repeat-offset)
// and delta matches (explicit and repeat-offset), exactly as wimlib's
// decompressor does, including the delayed LRU queue updates and adaptive
// Huffman code rebuilding.
//
// The encoder, by contrast, is intentionally simple and is NOT tuned for
// compression ratio or bitstream compatibility with wimlib's/Microsoft's
// encoder:
//
//   - It never uses delta matches.
//   - It never uses repeat-offset ("rep") matches, only explicit-offset LZ
//     matches, which sidesteps needing to replicate wimlib's delayed LRU
//     queue bookkeeping on the encode side (the decoder still implements
//     it fully, since it must decode real data that does use rep and delta
//     matches).
//   - Match finding is a simple greedy/lazy hash-chain search, not the
//     near-optimal parse wimlib's encoder performs.
//
// The resulting compressed streams are valid LZMS and decode correctly
// (verified both by round-trip through this package's own Decompress and
// by cross-checking against wimlib-imagex where practical), but they are
// larger than what wimlib or Microsoft's encoder would produce from the
// same input. See README.md for the real-data verification performed.
package lzms

// Compress compresses data using LZMS and returns the compressed bytes.
// The returned buffer contains only the raw LZMS bitstream (both the
// forward range-coded stream and the backward Huffman/verbatim-bit stream
// that together make up one LZMS block); it carries no WIM chunk framing,
// and the caller must separately record the uncompressed size in order to
// call Decompress.
func Compress(data []byte) []byte {
	return compress(data)
}

// Decompress decompresses an LZMS-compressed buffer produced by Compress
// (or by any other conformant single-block LZMS encoder, such as wimlib's
// or Microsoft's) into a buffer of exactly expectedSize bytes.
//
// expectedSize must be known ahead of time (e.g. from surrounding WIM
// metadata); LZMS's raw bitstream does not self-describe the decompressed
// length.
func Decompress(data []byte, expectedSize int) ([]byte, error) {
	return decompress(data, expectedSize)
}
