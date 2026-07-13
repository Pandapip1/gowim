# gowim/pa30

A Go decoder for Microsoft's "PA30" MSDELTA patch file format, restricted to
the **null-delta** case (empty source buffer, no preprocessing, empty base
rift table) -- the case `Windows\WinSxS\Manifests\*.manifest` files use
(they're self-compressed, not diffed against a prior version).

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
- **Null-delta only.** A non-empty base rift table, or an SRC/FULLSRC match
  (which references a prepended source buffer), returns an error rather
  than being decoded -- this package has no source-buffer/rift-table
  machinery at all.
- **No preprocessing.** A non-empty `preProcessBuffer` returns an error.

## Verification status

**This decoder has not yet been verified against a real WinSxS `.manifest`
file.** That requires an independent ground truth this project doesn't yet
have on hand -- a real `ApplyDeltaB`/`msdelta.dll` call on a Windows host,
or one of the wrapper tools (`wcpex`, `SXSEXP`) as an oracle. Until that
happens, treat this package as a plausible-but-unconfirmed implementation of
an undocumented format, not a trusted one. See `TODO.md` for the pending
verification step.

What testing *does* cover, all in `*_test.go`:

- The bit reader and variable-length integer decoder against the reference
  README's own worked bit-level example (real ground truth from the format
  spec's author, byte-for-byte).
- The canonical Huffman engine against hand-computed codewords (independent
  of this package's own code).
- The default code-length formula against hand-verified sizes.
- A full synthetic PA30 file (hand-built with a matching test-only bit
  writer) exercising the entire pipeline -- header, buffers, rift-table
  rejection, default Huffman lengths, and both literal and back-reference
  match decoding -- but this is a self-built file, not a real
  Windows-produced one, so it cannot catch a systematic misunderstanding of
  the real format shared between this package and its own tests.

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
