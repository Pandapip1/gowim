// Package xpress implements the XPRESS compression algorithm, as used for
// individually-compressed WIM resource chunks (one of the three compression
// types a WIM resource may declare, alongside LZX and LZMS).
//
// # Scope
//
// This is a pure, WIM-agnostic codec: Compress and Decompress operate on a
// single, already-delimited byte buffer and know nothing about the WIM
// container format. In particular this package deliberately does not
// implement:
//
//   - WIM chunk-table framing: WIM resources larger than a fixed chunk size
//     (conventionally 32768 or 65536 bytes) are split by the *container*
//     format into independently-compressed chunks, each with its own entry
//     in a chunk offset table. That splitting and the chunk table itself
//     are the responsibility of the wim package (or whatever future
//     wim-level code wires this codec in), not of this package. This
//     package's own window/match-offset limit (see below) is a property of
//     the XPRESS format itself, not a chunking policy it imposes.
//   - Compression-ratio optimality: Compress uses a simple greedy/lazy LZ77
//     match finder (see lz77.go) and always emits a valid canonical Huffman
//     code (see huffman.go), but it does not attempt wimlib-style
//     near-optimal parsing or bit-cost-aware code selection. LZ77+Huffman
//     streams are not required to be byte-identical to be equally correct
//     -- many encodings decode to the same output -- so Compress only
//     guarantees that Decompress(Compress(data), len(data)) reproduces data
//     exactly, not that it produces the smallest possible encoding.
//   - The "stored uncompressed" fallback WIM resources use when compression
//     would not shrink the data: that is a container-level policy decision
//     (comparing compressed vs. uncompressed size and choosing the
//     resource's on-disk representation accordingly), not something this
//     codec decides for itself. Compress always returns a real compressed
//     stream.
//
// # Which XPRESS variant this is
//
// [MS-XCA] ("Xpress Compression Algorithm", Microsoft Open Specification,
// https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-xca/)
// documents three related but distinct formats under the "XPRESS" name:
// "Plain LZ77" (flag-word-based, no Huffman coding, max match offset 8192),
// "LZ77+Huffman" (a single 512-symbol canonical Huffman code over LZ77
// literals/matches, max match offset 65535), and LZNT1 (a simpler,
// non-Huffman LZ77 variant used elsewhere in Windows, e.g. NTFS compression).
//
// WIM resources compressed with "XPRESS" use the LZ77+Huffman variant. This
// is stated directly by wimlib's decoder (src/xpress_decompress.c, commit
// cd5e231c348c255ae5088873b5a66ee0eb96fa07 of
// https://github.com/ebiggers/wimlib): "The format in WIMs is specifically
// the algorithm labeled as the 'LZ77+Huffman Algorithm'". This package's
// bitstream layout, canonical Huffman construction, and match/length
// encoding were all cross-checked directly against that wimlib source
// (src/xpress_decompress.c, src/xpress_compress.c,
// include/wimlib/xpress_constants.h, include/wimlib/decompress_common.h) at
// the same commit, and against real XPRESS-compressed WIM data produced by
// wimlib-imagex (see xpress_test.go) -- not against the MS-XCA prose alone,
// which describes the three variants at a high level but does not fully
// spell out the exact interleaved bit/byte layout implemented here (see
// bitwriter.go and bitreader.go for that layout and why it must be followed
// precisely for compatibility with real decoders).
//
// No discrepancies were found between the MS-XCA high-level description of
// the LZ77+Huffman variant (the alphabet shape, the two-stage
// header-then-coded-items layout, the match length/offset encoding) and
// wimlib's implementation; wimlib was used as the primary, byte-level
// reference simply because MS-XCA does not specify the bitstream
// interleaving at that level of detail.
package xpress
