# gowim/pa30

A Go decoder for Microsoft's "PA30" MSDELTA patch file format. Originally
scoped to the **null-delta** case (empty source buffer, no preprocessing,
empty base rift table) on the assumption that
`Windows\WinSxS\Manifests\*.manifest` files were self-compressed rather than
diffed against a prior version -- **real-data testing disproved that
assumption** (see "Verification status" below): real `.manifest` files are
compressed against a large (~9-10KB), shared, non-empty source buffer this
package does not yet have access to. This package correctly decodes header
fields and any content up to the point a real file references that external
buffer, then errors out rather than guessing.

## Why this exists, and its licensing approach

The on-the-wire PA30 format is not documented by Microsoft. A real, working,
from-scratch reference decoder exists --
[smilingthax/msdelta-pa30-format](https://github.com/smilingthax/msdelta-pa30-format)
-- but it has **no `LICENSE` file**, so its reuse terms are unresolved. To
avoid that ambiguity entirely, this package was written **clean-room**:

1. That repository's `README.md` was read in full -- it's a genuine,
   detailed bitstream specification (header layout, bit-reading order, the
   variable-length integer encoding, canonical Huffman parameters, match/slot
   decoding, a worked bit-level example), not just a code dump.
2. A background research agent read the repository's *C source* and
   answered a fixed list of precise implementation questions in prose
   (citing file/function for each answer, without quoting substantial code)
   -- filling in exactly the handful of details the README leaves slightly
   ambiguous (canonical code construction direction, the exact long-length
   bit layout, LRU queue update semantics, etc.).

No C source from that repository was read or transliterated by whoever wrote
this package's Go code -- only the README, plus natural-language
descriptions of the C code's behavior. See `TODO.md`'s "PA30 code-reading
methodology" section in the repo root for the full research trail this
package was built from.

## Scope

- **Decoding only.** No encoder. (`TODO.md` notes an encoder may not even be
  needed, if component removal only ever deletes `.manifest` files rather
  than rewriting them.)
- **No shared-dictionary support (yet).** A non-empty base rift table, or an
  SRC/FULLSRC match (which references a prepended source buffer), returns an
  error rather than being decoded -- this package has no source-buffer/
  rift-table machinery at all. This is the scope gap real `.manifest` files
  actually hit (see below), not a rare edge case.
- **No preprocessing.** A non-empty `preProcessBuffer` returns an error.

## Verification status

**This decoder has been checked against real WinSxS `.manifest` files**
(2026-07-13), using `msdelta-pa30-format`'s `dump` binary as an independent,
black-box ground-truth oracle: built and run, its stdout read, its source
*not* consulted for this check (only for the earlier clean-room
implementation pass above). This caught and fixed a real bug: this
package's canonical Huffman construction originally used the textbook
DEFLATE-style bottom-up threshold recurrence; PA30's real construction is
top-down (see `huffman.go`'s `huffmanTree` doc comment for the fix and why
it matters). With that fixed, header parsing, buffer extraction, and
Huffman/literal/back-reference decoding all match the reference oracle
exactly on every real file tried -- including reproducing the *exact*
output offset at which decoding must stop because the file references the
external shared source buffer described below.

**Major finding from this same verification pass: real `.manifest` files are
not null-delta.** Every sampled file (36 total) is compressed against a
shared, non-empty source buffer roughly 9-10KB in size (30 of them hit the
identical offset and output position, implying one universal baseline, not
a per-file previous-version diff). The buffer's origin is now identified,
not just suspected: it's **PE resource type 0x266 (decimal 614), name 1,
inside `wcp.dll`**, starting with `<?xml version='1.0'` -- confirmed by a
[Cobalt.io writeup](https://www.cobalt.io/blog/part-2-decoding-windows-cbs-manifests-building-the-decoder)
and matching this package's own empirical observation exactly. Obtaining
and using that buffer (this repo's `pe` package could plausibly extract it)
is the next step toward fully decoding real `.manifest` files -- not yet
attempted. See `TODO.md`'s CBS/servicing section for the full trail.

What testing covers, in `*_test.go`:

- The bit reader and variable-length integer decoder against the reference
  README's own worked bit-level example (real ground truth from the format
  spec's author, byte-for-byte).
- The canonical Huffman engine against hand-computed codewords (independent
  of this package's own code).
- The default code-length formula against hand-verified sizes.
- `TestDecodeRealManifestSample`: a real `.manifest` file, checked against
  the reference oracle's own output (header fields, and the exact output
  offset decoding correctly stops at).
- A full synthetic PA30 file (hand-built with a matching test-only bit
  writer) exercising the entire pipeline -- header, buffers, rift-table
  rejection, default Huffman lengths, and both literal and back-reference
  match decoding.

Still unverified: decoding once the shared source buffer is available (no
real file has been decoded fully end-to-end yet), and the multi-block
(non-`isDefault`) compression-parameters path, which real-data testing
hasn't exercised so far.

## Usage

```go
data, _ := os.ReadFile("some.manifest")

out, header, err := pa30.Decode(data)
if err != nil {
    log.Fatal(err) // e.g. non-null-delta patch, unsupported
}
// out is the decompressed manifest XML; parse with the sibling mum package.
```

## License

MIT OR Apache-2.0 (this package's own code; it contains no code from
`msdelta-pa30-format`).
