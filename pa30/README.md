# gowim/pa30

A Go decoder for Microsoft's "PA30" MSDELTA patch file format, used by
WinSxS `Windows\WinSxS\Manifests\*.manifest` files. Originally scoped to the
**null-delta** case (empty source buffer) on the assumption these files were
self-compressed rather than diffed against a prior version -- **real-data
testing disproved that** (see "Verification status" below): real
`.manifest` files are compressed against a large (~9-10KB), shared,
non-empty source buffer, now identified, extracted, and embedded as
`testdata/wcp_dictionary.bin`. `DecodeWithSource` **fully and correctly
decodes real `.manifest` files** whose content only needs DST/LRU
back-references into that buffer -- see `TestDecodeWithSourceRealManifestFullSuccess`.
SRC/FULLSRC matches (a separate addressing scheme) are now also decoded, but
**unverified against real data** -- see "SRC/FULLSRC" below.

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
- **DST/LRU back-references into a source buffer are supported**
  (`DecodeWithSource`). **SRC/FULLSRC matches are also decoded, but
  unverified against real data** -- see "SRC/FULLSRC" below. A non-empty
  base rift table is still unsupported regardless and returns an error.
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
it matters).

**Major finding from this same verification pass: real `.manifest` files are
not null-delta.** Every sampled file (36 total) is compressed against a
shared, non-empty source buffer roughly 9-10KB in size. Its origin is
identified: **PE resource type 0x266 (decimal 614), name 1, inside
`wcp.dll`** -- confirmed by a
[Cobalt.io writeup](https://www.cobalt.io/blog/part-2-decoding-windows-cbs-manifests-building-the-decoder)
and matching this package's own empirical observation exactly. Extracted
(via `wrestool`, a standard PE-resource tool) from a real `wcp.dll` and
embedded as `testdata/wcp_dictionary.bin`.

**With that dictionary, `DecodeWithSource` fully decoded a real `.manifest`
file end-to-end for the first time** -- cross-validated two independent
ways: the output length exactly matches the header's `TargetSize`, and its
SHA-256 exactly matches a `COMPONENTS`-hive `S256H` registry value this
project separately found (and, until this point, couldn't explain) for the
same component identity while reverse-engineering the `COMPONENTS` hive.
Two unrelated research threads in this project now corroborate each other.
See `TestDecodeWithSourceRealManifestFullSuccess` and `TODO.md`'s "S256H
mystery resolved" entry.

What testing covers, in `*_test.go`:

- The bit reader and variable-length integer decoder against the reference
  README's own worked bit-level example (real ground truth from the format
  spec's author, byte-for-byte).
- The canonical Huffman engine against hand-computed codewords (independent
  of this package's own code).
- The default code-length formula against hand-verified sizes.
- `TestDecodeRealManifestSample`: the same real `.manifest` file decoded
  *without* the dictionary, checked against the reference oracle's own
  output (header fields, and the exact output offset decoding correctly
  stops at without a source buffer).
- `TestDecodeWithSourceRealManifestFullSuccess`: the real, full,
  cross-validated end-to-end decode described above.
- A full synthetic PA30 file (hand-built with a matching test-only bit
  writer) exercising the entire pipeline -- header, buffers, rift-table
  rejection, default Huffman lengths, and both literal and back-reference
  match decoding.

Still unverified: the multi-block (non-`isDefault`) compression-parameters
path, which real-data testing hasn't exercised so far, and SRC/FULLSRC
matches (see below).

## SRC/FULLSRC (added 2026-07-13, unverified against real data)

Unlike everything else in this package, SRC/FULLSRC was **not** clean-room
implemented from `msdelta-pa30-format`'s README/prose -- there's none to
read: its own `dump.c` recognizes these match types' bitstream symbols but
never computes a real source address for them (only prints decoded length),
by its own admission a bitstream *dumper*, not a full decompressor, for this
match type. Two patents it cites (US6466999, US6938109B1) were read in full
and found to describe older/adjacent mechanisms (offline symbol-aware "rift
table" preprocessing; LZ77 history-window pre-loading), not PA30's actual
slot/delta bitstream encoding -- exhausted as a source for this gap.

Instead, a background agent statically disassembled the real, genuine
`msdelta.dll`'s `ApplyDeltaB` (a documented Win32 API; only its machine code
was read, since Microsoft ships no source for it -- ordinary black-box
disassembly, not a licensing concern like the `msdelta-pa30-format` question
above). Finding, implemented in `match.go`/`patch.go`: no persistent source
cursor exists -- each match resolves `sourcePos = targetPos - distance`
fresh, where `distance = delta` for SRC (slots 0-2) and `distance = 0` for
FULLSRC (slot 3); the rift table is confirmed (via embedded
pipeline-description strings in `msdelta.dll` referencing
`AddRiftEntry(emptyTable, sourceSize, 0)`) to be an identity no-op for
RAW/manifest content, making `distance` numerically interchangeable with the
`offset` DST/LRU matches already use.

Two pieces the disassembling agent itself flagged as not fully confirmed are
implemented per its best reading but explicitly called out in code comments
(see `match.go`'s `matchParams` doc comment): slot 2's bias constant, and
whether SRC/FULLSRC updates the DST/LRU repeat-offset queue. FULLSRC's
`distance=0` is suspicious on its face (a self-referential zero-offset
match) and is deliberately left to trip `decodeContent`'s existing
offset-validity check rather than silently reinterpreted. **No real sample
decoded so far actually exercises SRC/FULLSRC** -- see `TODO.md`'s
"Implement SRC/FULLSRC match decoding" entry for the full trail and what
would be needed to confirm this.

## Usage

```go
data, _ := os.ReadFile("some.manifest")
dict, _ := os.ReadFile("wcp_dictionary.bin") // see testdata/ for how to get one

out, header, err := pa30.DecodeWithSource(data, dict)
if err != nil {
    log.Fatal(err) // e.g. a non-empty base rift table this package doesn't support
}
// out is the decompressed manifest XML; parse with the sibling mum package.
```

## License

MIT OR Apache-2.0 (this package's own code; it contains no code from
`msdelta-pa30-format`).
