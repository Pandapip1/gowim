# gowim/cab

Reads Microsoft Cabinet (`.cab`) files: the CFHEADER/CFFOLDER/CFFILE/CFDATA
container format documented in Microsoft's
[\[MS-CAB\]: Cabinet File Format](https://learn.microsoft.com/en-us/previous-versions/bb417343(v=msdn.10))
-- **(i)** official Microsoft documentation, fetched and read in full
2026-09-14 -- plus a real CAB-LZX decoder for the one compression method
this package's own real motivating file actually uses.

This closes a gap the sibling `wufetch` package's own README explicitly
calls out: `wufetch` downloads a Capability's raw `.cab` bytes but performs
no extraction of its own ("No `.cab` extraction... needs an external tool
(`cabextract`, `7z`) or a real MS-CAB decoder, neither of which exists
elsewhere in gowim today"). This package is that decoder.

## Why a real LZX decoder, not just a container parser

The concrete motivating file --
`OpenSSH-Server-Package-amd64.cab` (`wufetch`'s own real, live-downloaded
target for Windows 11 25H2/build 26200/amd64, sha256
`fc40c1573a6dacd58c57e2d85fb6fe9b75a7883b054cc541aa9929453cf7c5cc`) -- was
not assumed to need any particular compression method. Its real CFFOLDER
header was decoded and independently cross-checked against `7z l -slt`'s own
report before writing a single line of decoder: both agree its one folder
is `LZX` with a 21-bit (2 MiB) window, not "stored"/uncompressed and not
MSZIP/Quantum -- **(iii)**, this package's own empirical check against the
real downloaded file, not an assumption from the FOD documentation (which
does not state a compression method at all).

## Why not reuse gowim's own sibling `lzx` package

`lzx` (see its own README's "WIM vs. CAB-LZX" section) implements only the
*WIM* flavor of LZX, and documents concretely why it isn't the same
bitstream framing as CAB-LZX:

- WIM resets the Huffman tables and match window every 32768-byte chunk;
  CAB-LZX keeps one persistent, circular window and continuous Huffman
  state across an entire CFFOLDER's CFDATA blocks (which is why this
  package's own decoder is written against one *folder*'s concatenated
  CFDATA payload, not block-by-block).
- WIM always applies the E8 (x86 CALL) address-translation filter with a
  fixed magic size; CAB-LZX signals it (and its translation size) once, via
  a header bit, and applies it per 32768-byte output "frame" using the
  *real* declared translation size.
- WIM's block-size field is a compact variable-width encoding; CAB-LZX
  always spends a fixed 24 bits.

These are real, structural differences (confirmed against libmspack's own
source, see below), not merely documentation-lint items, so this package
implements its own, independent CAB-LZX decoder (`cablzx.go`) rather than
attempting to bolt CAB framing onto `lzx`'s WIM-shaped decoder.

## Ported from libmspack, not from the \[MS-CAB\] prose

The CAB-LZX decoder (`cablzx.go`) is a direct, line-for-line port of
[libmspack](https://github.com/kyz/libmspack)'s `mspack/lzxd.c` --
**(ii)**, the real, independent, widely-used open-source implementation
that `cabextract` and `7z`'s own CAB-LZX support are built on -- rather than
from Microsoft's original "cab-sdk.exe" LZX specification prose. libmspack's
own header comment on `lzxd.c` documents multiple real discrepancies
between that prose and Microsoft's actual shipped implementation (position-
slot counts, the aligned-offset tree's position relative to the main/length
trees, the uncompressed block's length field not being documented at all,
and the E8 filter's real "last 10 bytes" boundary versus the spec's stated
"last 6"), and states its own code follows the real implementation's
behavior in every such conflict. This port does the same, for the same
reason. Only the plain-CAB path is ported (`is_delta=0`, `reset_interval=0`
-- CAB has no reset-interval field at all; that's a CHM-only extension), not
LZX DELTA or CHM's reset-interval variant, since cabinet files never use
either.

The CFDATA checksum algorithm (`cfdata.go`'s `cabChecksum`) is similarly
ported from libmspack's `mspack/cabd.c` `cabd_checksum`, not derived from
[MS-CAB]'s prose -- its tail-byte folding order
(`(data[0]<<16)|(data[1]<<8)|data[2]` for a 3-byte remainder, not the
little-endian order a naive reading of "checksum" might suggest) was
confirmed against every real CFDATA block in this package's own test
cabinet (several of which have a non-multiple-of-4 remainder), not assumed.

### Real bugs found and fixed against real data, not by inspection

Three real bugs were found this way, each because the *previous* version of
this package's own decoder failed to reproduce the real, byte-for-byte
extraction of `cabextract`/`7z` against the real test cabinet -- not by
reading libmspack more carefully first:

1. **Canonical-Huffman code assignment must never count zero-length
   ("unused") symbols into `count[0]`.** RFC 1951 section 3.2.2 states this
   explicitly ("the `bl_count[0]` entry should never be used"), but an
   initial implementation counted them anyway, corrupting every
   `next_code[l]` derivation and (for this package's real pretree, a
   20-symbol table) building a decode table with out-of-range indices.
2. **The input bitstream must be realigned to the next 16-bit coding-unit
   boundary at the end of *every* 32768-byte output frame**, not just after
   an `UNCOMPRESSED` block -- libmspack's `lzxd_decompress` does this
   unconditionally per frame. Missing this let frame 0 of the real test
   cabinet decode perfectly (32768 bytes, byte-for-byte correct) while
   frame 1 -- continuing mid-block, with no new block header read in
   between -- silently read from the wrong bit position, corrupting every
   byte of frame 1 onward. This is the one library-specific behavior this
   README calls out explicitly because it is easy to miss: block boundaries
   and frame boundaries are independent concepts in CAB-LZX, and only frame
   boundaries force a bit-realignment.
3. **A match's length is never split at a block boundary and can overrun
   the block's declared remaining length**, in which case the overrun must
   be subtracted from `block_remaining` afterward (libmspack: "did the
   final match overrun our desired this_run length?"). Skipping this
   correction let a block-completion decision fire one match too early,
   desynchronizing the rest of the bitstream from that point on.

## Scope

```go
r, err := cab.NewReader(cabinetBytes)   // parses CFHEADER/CFFOLDER/CFFILE eagerly
for _, f := range r.Files {
    fmt.Println(f.Name, f.Size)
}
data, err := r.Extract("OpenSSH-Server-Package~31bf3856ad364e35~amd64~~10.0.26100.1.mum")
// or, given a *File already in hand:
data, err := r.ExtractFile(f)
```

`NewReader` parses the whole directory (every `CFFOLDER` and `CFFILE`
record) up front; CFDATA is read and decompressed lazily, per folder, on
the first `Extract`/`ExtractFile` call that needs it, and memoized (multiple
files commonly share one folder -- all 38 files in this package's own real
test cabinet live in a single folder).

This package deliberately does not implement:

- **Writing/creating cabinets.** Read-only, matching this package's actual
  need (extracting a downloaded Capability payload).
- **Multi-cabinet spanning** (a folder's data continuing into or from a
  neighboring `.cab` in a set). `CFHEADER`'s `cfhdrPREV_CABINET`/
  `cfhdrNEXT_CABINET` flags and a `CFFILE`'s `iFolder` continuation markers
  (`0xFFFD`/`0xFFFE`/`0xFFFF`) are detected and rejected with
  `ErrMultiCabinet` rather than silently mishandled -- a single Capability
  `.cab` (this package's motivating case, and Microsoft's FOD `.cab`s in
  general) is always self-contained.
- **MSZIP or Quantum compression.** Only "stored" (`CompressNone`) and LZX
  are implemented -- the only two methods this package's real test data
  exercises. A folder using MSZIP/Quantum returns
  `ErrUnsupportedCompression` rather than guessing at a codec never
  verified against real data.
- **LZX DELTA / CHM's reset-interval extension.** See "Ported from
  libmspack" above.

## Tests

```
go test ./...
```

`cablzx_test.go`'s `TestRealCabinet_MatchesCabextract` decodes the real
`testdata/OpenSSH-Server-Package-amd64.cab` (the same file `wufetch`'s own
`TestRealDownload_OpenSSHServer26200Amd64` downloads and hash-verifies live;
a copy is committed here as `testdata` since this package's own tests need
its actual bytes, not just its hash) and diffs every one of its 38 files,
byte for byte, against `cabextract` -- a real, independent, widely-used
extractor -- actually extracting the same real file (skipped if
`cabextract` isn't installed). `TestRealCabinet_KnownFiles` additionally
asserts specific known file names/sizes (`sshd.exe`, `moduli`, the package
`.mum`) and that `sshd.exe`'s real `MZ` PE header survived decompression
intact, without requiring `cabextract` to be present.

## License

MIT OR Apache-2.0.
