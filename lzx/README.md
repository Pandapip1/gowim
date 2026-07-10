# lzx

A Go implementation of **LZX**, the Microsoft LZ77/Huffman compression format,
specifically the restricted flavor of it used by the **WIM (Windows Imaging
Format)** container. This is a pure, WIM-agnostic codec over raw byte
buffers — it knows nothing about WIM resource headers, chunk tables, or
multi-chunk framing; see [Scope](#scope).

Modeled on and cross-checked against
[wimlib](https://github.com/ebiggers/wimlib) commit
`cd5e231c348c255ae5088873b5a66ee0eb96fa07`, specifically
`include/wimlib/lzx_constants.h`, `include/wimlib/lzx_common.h`,
`src/lzx_common.c`, `src/lzx_decompress.c`, `src/lzx_compress.c`, and
`include/wimlib/decompress_common.h` — the real, shipped, independent
implementation that actual WIM files are compressed/decompressed against.
The older public "Microsoft LZX Data Compression Format" (1997) document and
the newer "[MS-PATCH]: LZX DELTA Compression and Decompression" (2014)
document were also consulted, but both describe different flavors of LZX
(CAB-file LZX, and LZX DELTA respectively) — see
[WIM vs. CAB-LZX](#wim-vs-cab-lzx) below for exactly where they disagree with
what WIM actually needs, and why wimlib's behavior was followed in every case
of conflict.

## Scope

```go
func Compress(data []byte) []byte
func Decompress(data []byte, expectedSize int) ([]byte, error)
```

Each call is one independent, stateless "chunk": no state (Huffman tables,
window contents, x86-filter position) is carried between calls, matching how
WIM divides a resource into (conventionally 32768-byte) chunks that are each
compressed independently. This was confirmed against a real Windows 11 23H2
install image on the development machine: `wimlib-imagex info` on its
`sources/boot.wim` reports `Compression: LZX` and `Chunk Size: 32768 bytes`.

This package **deliberately does not implement**:

- **WIM chunk-table framing or multi-chunk resource splitting.** A WIM
  resource larger than one chunk is a sequence of independently-compressed
  chunks addressed by a chunk offset table; wiring that up is a separate,
  future task at the `wim` package level. This package only ever sees (and
  only ever produces) one chunk's plaintext/compressed bytes per call.
- **CAB-LZX's sliding window / multi-block streaming model**, or LZX DELTA's
  reference-data extensions. Neither is used by WIM.
- **Compression-ratio or match-finding optimality.** `Compress` uses a
  straightforward greedy LZ77 match finder (bounded-depth hash chains, no
  lazy matching or optimal parse) and **always emits `VERBATIM` blocks** —
  it never builds an "aligned offset" Huffman tree, and never emits an
  `UNCOMPRESSED` block. This is a valid, spec-compliant subset: block type
  is signaled per block, and wimlib's own compressor already relies on this
  (its own comment notes it never emits `UNCOMPRESSED` blocks either), so
  any compliant decoder — including wimlib's — must already accept an
  all-`VERBATIM` stream. It is simply not the most compact possible
  encoding for a given input.
- **Window orders beyond 21** (buffers larger than 2097152 bytes = wimlib's
  own documented `LZX_MAX_WINDOW_ORDER`). `Compress` panics on larger input.
  WIM itself never asks for more than one 32768-byte chunk per call, so this
  is generous headroom rather than a real restriction.

`Decompress`, in contrast, supports all three LZX block types (`VERBATIM`,
`ALIGNED`, `UNCOMPRESSED`), since real-world data — including wimlib's own
`ALIGNED`-block output — must decode correctly regardless of what this
package's own encoder happens to produce.

The x86 "E8" call-instruction address-translation filter *is* implemented
and *is* always applied (see below) — it is required for this package's own
compressed output to be decodable by a real WIM/LZX decoder, and for this
package to decode real WIM data.

## WIM vs. CAB-LZX

wimlib's own `src/lzx_decompress.c` documents this directly, and `Compress`/
`Decompress` were verified against wimlib's actual behavior (not just the
prose) per [Verification](#verification) below. The differences that matter
for this package:

1. **No sliding window.** Each chunk uses a fixed window sized to the chunk
   itself (32768 bytes → window order 15 for WIM's conventional chunk size),
   with no window or Huffman-table continuity across chunks. CAB-LZX
   maintains one large sliding window and Huffman-table continuity across
   many blocks in a single stream.
2. **E8 filter is unconditional.** WIM applies the x86 CALL-instruction
   (`0xE8`) address-translation filter to *every* chunk, always, using a
   fixed magic "file size" parameter of `12000000` regardless of the
   resource's real size — there is no signal bit and no opt-out. CAB-LZX
   instead reserves an explicit header bit for whether the filter is
   enabled, and uses the real uncompressed size as the filter's parameter.
   (wimlib's comment additionally flags two long-standing errata in the
   1997 public spec here: the filter is not actually disabled after the
   32768th chunk as that document implies, and its "last 6 bytes" disabling
   description is imprecise — a 5-byte CALL instruction effectively cannot
   start in the last 10 bytes, not 6. This package's `e8filter.go` is a
   direct, byte-for-byte port of wimlib's own scalar reference
   implementation, so it inherits wimlib's — i.e. the real, ground-truth —
   behavior around these edges automatically, rather than re-deriving it
   from the erratum-laden spec text.)
3. **Compact block-size field.** WIM's block header spends 1 bit signaling
   "default size" (32768 bytes), then only pays for an explicit 16-bit (or,
   as a wimlib extension for window orders ≥ 16, 24-bit) size field
   otherwise. The original CAB-LZX format always spent a fixed 24 bits.
4. **wimlib's own compressor never emits `UNCOMPRESSED` blocks** (a design
   choice noted directly in its source, not a hard format restriction) —
   consistent with this package's own choice to only ever emit `VERBATIM`.

## Layout

| File | Responsibility |
|------|----------------|
| `lzx.go` | package doc, format constants/tables, `Compress`/`Decompress` entry points, window-order derivation |
| `bitreader.go` | LZX's bitstream convention: little-endian 16-bit coding units, bits consumed high-to-low |
| `bitwriter.go` | the mirror-image writer |
| `huffman.go` | canonical Huffman code construction (length-limited, for the encoder) and decoding (simple "first code per length" bit-at-a-time decoder) |
| `e8filter.go` | the x86 CALL-instruction (`0xE8`) address-translation filter, ported directly from wimlib's scalar reference implementation |
| `decode.go` | block-header parsing, block decoding (`VERBATIM`/`ALIGNED`/`UNCOMPRESSED`), LZ77 match copying |
| `encode.go` | block writing (`VERBATIM` only), codeword-length delta encoding |
| `matcher.go` | the greedy hash-chain LZ77 match finder |

## Usage

```go
import "github.com/Pandapip1/gowim/lzx"

compressed := lzx.Compress(original)

decompressed, err := lzx.Decompress(compressed, len(original))
if err != nil {
    log.Fatal(err)
}
// decompressed == original
```

To decode one chunk out of a real multi-chunk WIM resource, a caller (e.g. a
future `wim`-level integration) needs to slice out exactly that chunk's
compressed bytes using the resource's chunk offset table, and knows the
chunk's uncompressed size from the resource header / chunk size convention;
this package does not do that slicing itself (see [Scope](#scope)).

### A note on incompressible data

If `Compress`'s output ends up no smaller than the input (only possible for
near-incompressible data), a real WIM container is expected to store that
chunk *uncompressed* instead of using the (larger) LZX output — this was
observed directly: `wimlib-imagex capture --compress=LZX` over a file of
random bytes stores it with `flags=0` (uncompressed) in the resulting WIM,
not as an LZX chunk. That policy decision belongs to a WIM resource writer
(a future `wim`-level task), not to this codec: `Compress`/`Decompress`
remain correct either way (this package's own round-trip and ground-truth
tests include incompressible random data), it is simply wasteful to prefer
the compressed form when it is not actually smaller.

## Verification

Besides synthetic round-trip tests (`lzx_test.go`), this package's decoder
and encoder were both verified against real data using
[wimlib](https://wimlib.net) (`wimlib-imagex`, confirmed present as version
1.14.4 on the development machine) and a real Windows 11 23H2 install image
mounted at `/media/gavin-john/tiny11 23H2 x64`:

1. **Decoding real, pre-existing WIM data.** Using this repo's `wim`
   package to parse `sources/boot.wim`'s header, blob table, and (also
   LZX-compressed) image metadata resource, then manually walking each
   resource's chunk offset table (per wimlib's `src/resource.c`) to isolate
   individual chunks' compressed bytes: this package's `Decompress`
   correctly decoded the blob table (14274 entries), the directory-entry
   tree metadata resource, and several real files of varying size —
   `desktop.ini` (174 bytes, 1 chunk), `setup.exe` (95712 bytes, 3 chunks,
   x86 code exercising the E8 filter), and `TabTip.exe` (571744 bytes, 18
   chunks) — byte-for-byte identical to `wimlib-imagex extract`'s
   independent output for the same files. One of the two real-data fixtures
   used for this (the `desktop.ini` chunk, 154 real compressed bytes) is
   embedded directly in `lzx_test.go` (`TestDecompressRealWIMGroundTruth`)
   so `go test` exercises real ground-truth data with no external
   dependencies.
2. **Decoding freshly wimlib-encoded data.** `wimlib-imagex capture
   --compress=LZX` over a small directory of varying content (plain text,
   highly repetitive bytes, random/incompressible bytes, and a real x86
   executable) produced a fresh WIM; this package's `Decompress` correctly
   decoded every resulting chunk (including one that wimlib itself chose to
   store uncompressed, per the incompressible-data note above) against the
   known original files.
3. **Encoding, verified by wimlib itself.** This package's own `Compress`
   output was embedded in a hand-assembled minimal single-file WIM (built
   directly with this repo's `wim` package primitives:
   `ImageMetadata.AppendTo`, `BlobTable.AppendTo`, `Header.AppendTo`, and
   `DirEntry.Add`), and `wimlib-imagex extract` — a real, independent
   implementation this package's encoder was not directly derived from at
   the bit level — successfully decoded it back to the original bytes.
   This was exercised for plain text, x86 machine code (a slice of a real
   executable, exercising the E8 filter end-to-end through a real
   decoder), and repetitive data. This is the strongest verification
   performed: it confirms `Compress`'s output is not merely
   self-consistent with this package's own `Decompress`, but is valid input
   to a real, independent LZX decoder.

## Tests

```
go test ./...
```

Synthetic coverage: empty input, a single byte (including a single `0xE8`
byte), highly repetitive data, all-zero data, random incompressible data,
data at/around the 32768-byte conventional WIM chunk-size boundary (and the
16-bit block-size-field boundary at 65536), and x86-code-like data with
embedded `CALL rel32` instructions (exercising the E8 filter end-to-end
through `Compress`/`Decompress`). Plus the embedded real-WIM ground-truth
fixture described above. Compression ratio and E8-filter usage are not
asserted to be optimal anywhere — only round-trip correctness (see
[Scope](#scope)).

## License

MIT OR Apache-2.0.
