// Package pa30 implements a decoder for Microsoft's "PA30" MSDELTA patch
// file format. It was originally scoped to the null-delta case only (empty
// source buffer, no preprocessing transforms, empty base rift table), on
// the assumption that WinSxS `Windows\WinSxS\Manifests\*.manifest` files
// were self-compressed rather than diffed against a prior version. Real
// data (see "Verification status" below) disproved that assumption: real
// `.manifest` files are compressed against a large (~9-10KB), shared,
// non-empty source buffer. This package's scope is therefore narrower than
// originally intended -- it correctly decodes header fields and any
// literal/back-reference content up to the point a real file's compressed
// stream references that external source buffer, then errors out rather
// than guessing (see the top-level TODO.md's CBS/servicing section for the
// broader context, and the pending work to actually obtain and use that
// buffer).
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
// With that fixed, this package's header parsing, buffer extraction, and
// Huffman/literal/back-reference decoding match the reference oracle
// exactly on every real file tried, including reproducing the exact output
// offset at which decoding must stop because the file references the
// shared, currently-unsupported source buffer (see the package-level scope
// note above). See TestDecodeRealManifestSample for the regression test
// built from this real data. What remains unverified: decoding once the
// shared source buffer is actually available (no real file has been fully,
// end-to-end decoded yet), and the multi-block (non-isDefault) compression-
// parameters path, which real-data testing so far hasn't exercised.
//
// # Non-goals
//
// This package does not implement:
//
//   - Non-null-delta patches: a non-empty base rift table, SRC/FULLSRC
//     matches (which reference a prepended source buffer), or any
//     preprocessing transform all return errors rather than being decoded.
//   - Encoding. Only decoding is implemented; see TODO.md's note that an
//     encoder may not even be needed if component removal only ever
//     deletes `.manifest` files rather than rewriting them.
package pa30
