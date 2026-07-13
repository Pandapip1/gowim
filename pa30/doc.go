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
// example). SRC/FULLSRC matches are now also decoded, via a distance-based
// formula reverse-engineered from real msdelta.dll disassembly rather than
// from any documentation or reference implementation (neither exists for
// this specific piece -- see match.go's matchParams doc comment for full
// provenance) -- but this path remains UNVERIFIED against any real sample,
// since none sampled so far actually needs it; see "Non-goals" below. Decode
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
// What remains unverified: SRC/FULLSRC matches (see below), and the
// multi-block (non-isDefault) compression-parameters path, which real-data
// testing so far hasn't exercised.
//
// # SRC/FULLSRC (added 2026-07-13, unverified)
//
// Unlike everything else in this package, SRC/FULLSRC decoding was not
// clean-room-implemented from the reference tool's README/prose, because
// there is none to read: the reference `msdelta-pa30-format` tool's own
// `dump.c` never computes a SRC/FULLSRC source address either (it only
// prints their decoded length), by its own admission a bitstream dumper
// rather than a full decompressor for this match type. Two public patents
// cited by that tool (US6466999, US6938109B1) were read directly but
// turned out to describe older/adjacent mechanisms (offline symbol-aware
// "rift table" generation; LZ77 history-window pre-loading) rather than
// PA30's actual slot/delta bitstream encoding -- exhausted as a source for
// this specific gap.
//
// Instead, a background agent statically disassembled the real, genuine
// `msdelta.dll`'s `ApplyDeltaB` (a documented Win32 API; only its machine
// code was read, since Microsoft ships no source for it -- ordinary
// black-box disassembly, not a licensing concern) and traced its match
// dispatch and copy-address computation. Its finding, implemented in
// match.go/patch.go: `sourcePos = targetPos - distance`, with
// `distance = delta` for SRC (slots 0-2) and `distance = 0` for FULLSRC
// (slot 3); the rift table that would otherwise perturb this is an
// identity no-op for RAW/manifest content (confirmed via embedded
// pipeline-description strings referencing `AddRiftEntry(emptyTable,
// sourceSize, 0)`), making `distance` numerically interchangeable with the
// `offset` slots 4+ already use.
//
// This is REVERSE-ENGINEERED FROM DISASSEMBLY, NOT VERIFIED AGAINST ANY
// REAL SAMPLE -- no `.manifest` file sampled so far actually exercises
// SRC/FULLSRC. Two specific pieces are flagged by the disassembling agent
// itself as not fully confirmed (see match.go's matchParams doc comment
// for the full detail): slot 2's bias constant, and whether SRC/FULLSRC
// matches update the DST/LRU repeat-offset queue. A non-empty base rift
// table remains unsupported regardless (still returns an error) -- every
// real file sampled so far has an empty one even when its content needs
// SRC/FULLSRC, so that's an independent scope gap.
//
// # Non-goals
//
// This package does not implement:
//
//   - A non-empty base rift table -- returns an error rather than being
//     decoded. Every real file sampled so far has an empty one.
//   - Non-empty preprocessing buffers -- return an error.
//   - Encoding. Only decoding is implemented; see TODO.md's note that an
//     encoder may not even be needed if component removal only ever
//     deletes `.manifest` files rather than rewriting them.
package pa30
