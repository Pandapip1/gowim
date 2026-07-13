// Package pa30 implements a decoder for Microsoft's "PA30" MSDELTA patch
// file format, used by WinSxS `Windows\WinSxS\Manifests\*.manifest` files.
// It was originally scoped to the null-delta case only (empty source
// buffer), on the assumption that these files were self-compressed rather
// than diffed against a prior version. Real data disproved that: real
// `.manifest` files are compressed against a large (~9-10KB), shared,
// non-empty source buffer -- confirmed (2026-07-13) to be PE resource type
// 614 (0x266), name 1, inside `wcp.dll`, extracted via `wrestool` (a
// standard, documented PE-resource extraction) and embedded as
// testdata/wcp_dictionary.bin. DecodeWithSource takes that buffer directly
// and, as of this fix, fully and correctly decodes real `.manifest` files
// whose content only needs DST/LRU back-references into it (see
// TestDecodeWithSourceRealManifestFullSuccess for a real, cross-validated
// example). Files whose content needs SRC/FULLSRC matches (which use a
// delta/rift-offset addressing scheme this package does not implement) still
// return an error rather than being decoded -- see "Non-goals" below. Decode
// remains available for the plain null-delta case (source omitted/nil).
//
// # Provenance
//
// The on-the-wire PA30 bitstream format is not documented by Microsoft.
// This package's understanding of it comes entirely from a clean-room
// reading of a third-party reference decoder's documentation and behavior,
// NOT from that decoder's source code directly:
// https://github.com/smilingthax/msdelta-pa30-format has no LICENSE file
// (its reuse terms are unresolved), so its C source was deliberately never
// read or transliterated by whoever wrote this package. Instead:
//
//  1. That repository's README.md (a real, detailed bitstream
//     specification, including a worked example this package's tests
//     verify against directly) was read in full.
//  2. A background research agent read the repository's C source and
//     answered a fixed list of precise implementation questions in prose
//     (bit-reading order, the variable-length integer encoding, canonical
//     Huffman code construction, default code lengths, RLE-delta length
//     coding, match/slot decoding, the length tree's long-length encoding,
//     and LRU queue update semantics), citing file/function for each
//     answer but without quoting substantial code -- a description of
//     behavior to implement independently from, not a code copy.
//
// This package is therefore a from-scratch Go implementation, not a port.
// See TODO.md's "PA30 code-reading methodology" and "Component store
// implementation plan" sections for the full research trail.
//
// # Verification status
//
// This decoder HAS been checked against real WinSxS `.manifest` files
// (2026-07-13), using github.com/smilingthax/msdelta-pa30-format's `dump`
// binary as an independent, black-box ground-truth oracle (built and run,
// its stdout read -- its source was not consulted for this check, only for
// the earlier clean-room implementation pass described above). This caught
// and fixed a real bug: this package's canonical Huffman code construction
// originally used the textbook DEFLATE-style bottom-up threshold
// recurrence (shortest code length gets the smallest code values); PA30's
// real construction is top-down (longest code length gets the smallest
// values, built via a halving recurrence) -- see huffman.go's type doc.
//
// With the dictionary in hand, DecodeWithSource fully decoded a real
// `.manifest` file end-to-end for the first time, cross-validated two
// independent ways: the output length exactly matches the header's
// TargetSize, and its SHA-256 exactly matches a `COMPONENTS`-hive `S256H`
// registry value this project separately found (and, until this point,
// could not explain) for the same component identity while
// reverse-engineering the COMPONENTS hive -- see TestDecodeWithSourceRealManifestFullSuccess
// and TODO.md's "S256H mystery resolved" entry. Two unrelated research
// threads in this project now corroborate each other.
//
// What remains unverified: files whose content needs SRC/FULLSRC matches
// (see "Non-goals"), and the multi-block (non-isDefault) compression-
// parameters path, which real-data testing so far hasn't exercised.
//
// # Non-goals
//
// This package does not implement:
//
//   - SRC/FULLSRC matches (a delta/rift-offset addressing scheme, distinct
//     from the DST/LRU back-references this package does support even with
//     a source buffer) or a non-empty base rift table -- both return errors
//     rather than being decoded. Every real file sampled so far has an
//     empty base rift table regardless of whether its content needs
//     SRC/FULLSRC, so these are independent scope gaps, not the same one.
//   - Non-empty preprocessing buffers -- return an error.
//   - Encoding. Only decoding is implemented; see TODO.md's note that an
//     encoder may not even be needed if component removal only ever
//     deletes `.manifest` files rather than rewriting them.
package pa30
