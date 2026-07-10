# gowim/xpress

A pure, WIM-agnostic Go implementation of the **XPRESS compression
algorithm** (Microsoft's "LZ77+Huffman" variant, per
[\[MS-XCA\]](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-xca/)),
the codec WIM uses when a resource's compression type is XPRESS. Cross-checked
against [wimlib](https://wimlib.net) (source:
<https://github.com/ebiggers/wimlib>) commit `cd5e231c348c255ae5088873b5a66ee0eb96fa07`
and verified against real XPRESS-compressed data produced by `wimlib-imagex`
(see Tests below).

## Scope

This package compresses and decompresses a **single, already-delimited byte
buffer**. It knows nothing about the WIM container format and deliberately
does not implement:

- **WIM chunk-table framing.** WIM resources larger than a fixed chunk size
  (conventionally 32768 or 65536 bytes) are split by the container format
  into independently-compressed chunks, each with its own entry in a chunk
  offset/size table. That splitting, and the chunk table itself, are the
  responsibility of the `wim` package (or whatever code wires this codec in
  at that layer) - not this package. This package's own match-offset limit
  (65535, see `constants.go`) is a property of the XPRESS bitstream format
  itself, not a chunking policy it imposes; buffers larger than that can
  still be compressed correctly, they just won't find matches beyond that
  distance.
- **Compression-ratio optimality.** `Compress` uses a simple greedy/lazy LZ77
  match finder (`lz77.go`) and always emits a valid canonical Huffman code
  (`huffman.go`), but does not attempt near-optimal parsing or bit-cost-aware
  symbol selection the way wimlib's compressor does. LZ77+Huffman streams
  are not required to be byte-identical to be equally correct - many
  encodings decode to the same output - so `Compress` only guarantees that
  `Decompress(Compress(data), len(data))` reproduces `data` exactly, not that
  it produces the smallest possible encoding.
- **The "stored uncompressed" container fallback.** Real WIM resources (and
  individual chunks within them) that don't shrink under compression are
  stored raw instead; that's a container-level policy decision, not this
  codec's to make. `Compress` always returns a real XPRESS stream; a caller
  that wants the "use whichever is smaller" behavior compares
  `len(Compress(data))` against `len(data)` itself.

## Which XPRESS variant, and why

[MS-XCA] documents three related formats under the "XPRESS" name: "Plain
LZ77" (flag-word based, no Huffman coding, 8192-byte max match offset),
"LZ77+Huffman" (a single 512-symbol canonical Huffman code over LZ77
literals/matches, 65535-byte max match offset), and LZNT1 (a simpler
non-Huffman variant used elsewhere in Windows, e.g. NTFS compression).

**WIM resources use LZ77+Huffman.** wimlib's own decoder says so directly
(`src/xpress_decompress.c`: "The format in WIMs is specifically the algorithm
labeled as the 'LZ77+Huffman Algorithm'"), and this package's bitstream
layout, canonical Huffman construction, and match/length encoding were all
cross-checked directly against that source (`src/xpress_decompress.c`,
`src/xpress_compress.c`, `include/wimlib/xpress_constants.h`,
`include/wimlib/decompress_common.h` at the commit above) - not just the
high-level MS-XCA prose, which doesn't fully spell out the exact interleaved
bit/byte layout (see `bitwriter.go`/`bitreader.go` for that layout and why it
must be followed exactly, bit for bit, for compatibility with real
decoders). No discrepancies were found between MS-XCA's high-level
description and wimlib's implementation for this variant.

## Layout

One file per concern, following this repo's convention:

| File | Responsibility |
|------|-----------------|
| `xpress.go` | package doc: sources, scope, which XPRESS variant |
| `constants.go` | alphabet/match-length/offset constants |
| `bitwriter.go` | the interleaved coding-unit/raw-byte output stream |
| `bitreader.go` | its decode-side counterpart |
| `huffman.go` | canonical Huffman code construction (encode) and decode table (decode); the 256-byte codeword-length header pack/unpack |
| `lz77.go` | the hash-chain greedy/lazy LZ77 match finder used by `Compress` |
| `encode.go` | `Compress` |
| `decode.go` | `Decompress` |
| `xpress_test.go` | synthetic round-trip tests plus real wimlib ground-truth tests |

## Usage

```go
import "github.com/Pandapip1/gowim/xpress"

compressed := xpress.Compress(data)

// expectedSize is known ahead of time from the container format (e.g. a WIM
// resource header's uncompressed-size field).
decompressed, err := xpress.Decompress(compressed, len(data))
```

## Tests

```
go test ./...
```

Tests fall into three groups:

- **Synthetic round trips** (`TestRoundTripSynthetic`): empty input, a single
  byte, highly repetitive data, random incompressible data, all-zero data,
  and buffers at and immediately around the two chunk sizes WIM conventionally
  uses (32768 and 65536 bytes) plus arbitrary larger sizes - even though this
  package has no chunk-size concept of its own, these sizes are worth
  covering since they're what a `wim`-level caller will actually pass.
- **Real ground truth** (`TestGroundTruthWimlibXpress`): decodes real
  XPRESS-compressed byte streams captured from an actual WIM built by
  `wimlib-imagex` (wimlib 1.14.4) from real files taken from a Windows 11
  23H2 installation image (`wimlib-imagex capture <dir> out.wim <name>
  --compress=xpress`), extracted via this repo's sibling `wim` package
  (reading the blob table and then each resource's raw compressed bytes via
  `Reader.ResourceReader`) for resources small enough to be exactly one
  XPRESS stream with no WIM chunk-table framing. Each fixture's decompressed
  SHA-1 is checked against the known-correct hash of the original file. This
  is what proves `Decompress` implements the format real, independent
  software actually produces - not merely that it's self-consistent with
  this package's own `Compress`.
- **Encoder verification against a real, independent decoder**
  (`TestCompressThenWimlibReadable`): during development, `Compress`'s output
  for repetitive data, random incompressible data, a single-byte input, and a
  buffer just under the WIM chunk size was embedded in a hand-built minimal
  WIM (header + blob table + metadata resource + XML data, built directly
  from the sibling `wim` package's `AppendTo`/`Add` primitives) and confirmed
  to extract byte-for-byte correctly with the real `wimlib-imagex extract`
  - the strongest available check that the encoder produces bitstreams a
  genuinely independent, widely-deployed decoder accepts. That external tool
  isn't invoked at `go test` time (this package has no test-time dependency
  on wimlib-imagex or the `wim` module), so `TestCompressThenWimlibReadable`
  itself only re-asserts the same round trips against this package's own
  `Decompress`; the wimlib-imagex verification is recorded here and in
  `xpress.go` as a one-time result rather than as a repeatable, hermetic test.

One discrepancy surfaced during this verification and is worth calling out
explicitly: wimlib's own chunk reader (`resource.c`) rejects any compressed
chunk whose compressed size exceeds its uncompressed size (real WIMs instead
store such chunks raw). That's a container-level invariant, not a defect in
the XPRESS bitstream itself - `Compress`'s output is a valid XPRESS stream
regardless of whether it happens to be larger than the input - but it means
a hand-built test WIM (or a real `wim`-level integration) must apply the
"store raw if compression didn't shrink the data" policy per chunk, exactly
as the "stored uncompressed" scope note above describes.

## License

MIT OR Apache-2.0.
