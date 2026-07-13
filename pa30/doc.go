// Package pa30 implements a decoder for Microsoft's "PA30" MSDELTA patch
// file format, restricted to the null-delta case (empty source buffer, no
// preprocessing transforms, empty base rift table) used by WinSxS
// `Windows\WinSxS\Manifests\*.manifest` files -- these are self-compressed,
// not diffed against a prior version, so only that narrow case is needed
// here (see the top-level TODO.md's CBS/servicing section for the broader
// context this feeds into).
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
// This decoder has NOT yet been verified against real WinSxS `.manifest`
// bytes (that requires an independent ground truth -- a real
// `ApplyDeltaB`/`msdelta.dll` call on a Windows host, or a wrapper tool like
// wcpex/SXSEXP -- see TODO.md). Tests here check the bit reader and integer
// decoding directly against the README's own worked example, and check the
// Huffman engine and full decode pipeline for internal self-consistency
// (hand-computed canonical codes, and round-tripping hand-built synthetic
// patches), but that is not the same as confirming this matches Microsoft's
// real, undocumented on-the-wire format.
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
