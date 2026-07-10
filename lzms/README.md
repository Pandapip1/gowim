# lzms

A Go implementation of **LZMS**, the range-coded LZ77/delta compression
format Microsoft introduced in Windows 8, used by the **WIM (Windows
Imaging Format)** container for "solid" resources and `.esd` files. This is
a pure, WIM-agnostic codec over raw byte buffers — it knows nothing about
WIM resource headers, chunk tables, or solid-resource multi-blob framing;
see [Scope](#scope).

LZMS has never been documented by Microsoft. Unlike XPRESS (which has the
official MS-XCA specification) or LZX (which has an older public CAB-format
specification this repo's sibling `lzx` package cross-checks against), the
only reliable account of the wire format is
[wimlib](https://github.com/ebiggers/wimlib), which reverse-engineered it
from real compressed data and Windows's own `COMPRESS_ALGORITHM_LZMS`
behavior. This package is modeled on and cross-checked against wimlib
commit `cd5e231c348c255ae5088873b5a66ee0eb96fa07`, specifically
`src/lzms_decompress.c`, `src/lzms_compress.c`, `src/lzms_common.c`,
`include/wimlib/lzms_constants.h`, `include/wimlib/lzms_common.h`, and
`src/compress_common.c` (`make_canonical_huffman_code`, needed for the exact
Huffman-tree tie-break rule) — the real, shipped, independent
implementation that actual WIM/ESD files are compressed/decompressed
against. LZMS is a distinct bitstream from LZMA/7-Zip despite superficial
similarities (range coding, adaptive probabilities); every algorithmic
decision here was taken directly from wimlib's source, not from general
recollection of LZMA.

## Scope

```go
func Compress(data []byte) []byte
func Decompress(data []byte, expectedSize int) ([]byte, error)
```

This package implements only "raw" single-block LZMS compression and
decompression, matching `COMPRESS_ALGORITHM_LZMS | COMPRESS_RAW` in
Windows's own compression API. It **deliberately does not implement**:

- **WIM chunk-table or "solid resource" framing.** WIM splits LZMS data into
  chunks (conventionally 131072 bytes uncompressed — confirmed via
  `wimlib-imagex info` on a real Windows 11 23H2 `install.esd`) and, for
  solid resources, packs multiple file blobs into one compressed stream;
  both are container-level concerns for a future `wim`-package task (see
  `wim.BlobTable`'s `SolidResourceRun`), not this package. `Compress`/
  `Decompress` operate on exactly one already-delimited buffer.
- **Cross-call persistence of adaptive state.** LZMS's range coder and
  probability/Huffman models are inherently stateful across an entire
  compressed buffer, but that state lives only within a single `Compress`
  or `Decompress` call and is discarded afterward — each call starts fresh.

### Encoder limitations

The **decoder** is a complete, faithful implementation of the format: it
decodes literals, LZ matches (explicit and repeat-offset) and delta matches
(explicit and repeat-offset), exactly as wimlib's decompressor does,
including the delayed LRU-queue updates and adaptive Huffman code
rebuilding. This is deliberate: a decoder that correctly reads real
Windows/wimlib-produced LZMS data is far more valuable than an encoder that
matches wimlib's compression ratio, and it was prioritized accordingly (see
[Verification](#verification)).

The **encoder**, by contrast, is intentionally simple and is not tuned for
compression ratio or bitstream compatibility with wimlib's/Microsoft's
encoder:

- It never uses delta matches.
- It never uses repeat-offset ("rep") matches, only explicit-offset LZ
  matches — this sidesteps needing to replicate wimlib's delayed LRU-queue
  bookkeeping on the encode side (the decoder still implements it fully,
  since it must decode real data that does use rep and delta matches).
- Match finding is a simple greedy/lazy hash-chain search, not the
  near-optimal parse wimlib's encoder performs.

The resulting compressed streams are valid LZMS and decode correctly
(verified by round-trip through this package's own `Decompress`), but they
are larger than what wimlib or Microsoft's encoder would produce from the
same input, and — unlike the sibling `lzx` package — the encoder's output
has not been independently verified against `wimlib-imagex extract`; that
remains a known gap rather than a claimed guarantee. Decoder correctness
against real data has been verified (below) and is the load-bearing claim
this package makes.

## Layout

| File | Responsibility |
|------|----------------|
| `lzms.go` | package doc, sources/citations, `Compress`/`Decompress` entry points |
| `rangecoder.go` | the range coder (encode/decode primitives, renormalization) |
| `probmodel.go` | the adaptive binary probability model (6-bit recent-bits shift register) |
| `huffman.go` | adaptive Huffman code construction/rebuilding and decoding |
| `tables.go` | offset/length slot tables and related constants |
| `slots.go` | slot lookup helpers |
| `x86filter.go` | the x86 call/jump address-translation filter |
| `decode.go` | item decoding: literals, LZ matches, delta matches, repeat-offset LRU queues |
| `encode.go` | the (intentionally simple) encoder |
| `lzms_test.go` | synthetic round-trip tests plus the real ground-truth test |
| `testdata/appcompat.xsl`, `testdata/appcompat.xsl.lzms` | a real plaintext/compressed pair (see below) |

## Usage

```go
import "github.com/Pandapip1/gowim/lzms"

compressed := lzms.Compress(original)

decompressed, err := lzms.Decompress(compressed, len(original))
if err != nil {
    log.Fatal(err)
}
// decompressed == original
```

`Decompress` needs the uncompressed size ahead of time (e.g. from
surrounding WIM metadata) — LZMS's raw bitstream does not self-describe the
decompressed length.

## Verification

Besides synthetic round-trip tests (`lzms_test.go`: empty input, a single
byte, highly repetitive data, all-zero data, random incompressible data,
text-like and binary-like data, and data at/around the 131072-byte
conventional WIM chunk-size boundary), the decoder was verified against
real, independently-produced LZMS data using
[wimlib](https://wimlib.net) (`wimlib-imagex`, version 1.14.4) and a real
Windows 11 23H2 install image mounted at `/media/gavin-john/tiny11 23H2 x64`:

A small directory of real files from the mounted image (an XSL stylesheet,
a `.cat` catalog file, two DLL/database-like binaries, and synthetic
repetitive/random fixtures) was captured with
`wimlib-imagex capture ... --compress=lzms` to produce a real WIM compressed
by wimlib's own encoder. Using this repo's `wim` package to parse the
resulting file's header/blob table/directory tree and pull out each file's
raw compressed resource bytes (parsing the multi-chunk resource
chunk-offset table by hand where a file spanned more than one 131072-byte
chunk — `appraiser.sdb` at 2757956 bytes spans multiple chunks), this
package's `Decompress` correctly decoded every file — including the
multi-chunk one — byte-for-byte identical to both the real original file on
disk and the SHA-1 recorded in the WIM's own directory entry.

One of these real ground-truth pairs — `appcompat.xsl` (11673 bytes
plaintext, a single chunk since it's under the 131072-byte threshold, 1038
bytes compressed) — is embedded directly in this package via `go:embed`
(`testdata/appcompat.xsl` and `testdata/appcompat.xsl.lzms`) and exercised
by `TestDecompressRealWimlibGroundTruth`, so `go test` re-verifies real-world
decoder correctness on every run with no external dependencies (no mounted
ISO, no `wimlib-imagex` binary needed at test time). Binary fixture files
are used directly rather than a text-encoded (e.g. base64) literal in the
Go source.

The encoder was not independently verified against a real decoder (e.g. by
round-tripping this package's `Compress` output through `wimlib-imagex
extract`, as the sibling `lzx` package's encoder was) — see
[Encoder limitations](#encoder-limitations). This is a known, explicitly
stated gap, not an oversight.

## Tests

```
go test ./...
```

## License

MIT OR Apache-2.0.
