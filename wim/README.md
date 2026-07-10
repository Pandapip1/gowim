# gowim

A Go reimplementation of the on-disk handling of the **WIM (Windows Imaging
Format)** container: parsing, serialization, and the high-level structure.
Modeled on [wimlib](https://wimlib.net) (source:
<https://github.com/ebiggers/wimlib>), cross-checked against wimlib commit
`cd5e231` (2026-01-29).

## Scope

This package handles the **structure** of a WIM. It reads and writes the
container skeleton:

- the WIM header (`Header`)
- the resource header primitive (`ResourceHeader`)
- the blob / lookup table, including solid-resource run grouping (`BlobTable`)
- per-image metadata resources: the security-descriptor table (`SecurityData`)
  and the directory-entry tree (`DirEntry`, via `ImageMetadata`)
- the XML data (`XMLData`, UTF-16LE with a byte-order mark)
- the integrity table (`IntegrityTable`)
- a top-level reader over an `io.ReaderAt` (`Reader`)
- path-based operations over a `DirEntry` tree: case-insensitive lookup
  (`DirEntry.Child`, `DirEntry.Lookup`), reading a file's resolved contents
  (`Reader.ReadFile`, `BlobTable.ByHash`), adding/replacing a file
  (`DirEntry.Add`), deleting a file or subtree (`DirEntry.Remove`),
  moving/renaming (`DirEntry.Rename`), listing a directory (`DirEntry.ReadDir`),
  and DOS-style name globbing (`MatchName`)

**Compression is supported for non-solid resources of all three WIM codecs**
(XPRESS, LZX, LZMS): `wim` depends on the sibling `github.com/Pandapip1/gowim/xpress`,
`.../lzx`, and `.../lzms` modules (see `go.mod`'s `require`/`replace` entries,
matching the pattern `driver/go.mod` already uses for its own sibling
dependencies) to actually decompress and compress resource payloads. This is a
deliberate, intentional architecture change: `wim` is no longer a leaf module,
and that's correct, not an oversight.

- `DecodeResourceData` parses a compressed resource's on-disk chunk-table
  framing and dispatches each chunk to the right codec; `Reader.resourceData`
  calls it transparently, so `Reader.XMLData`, `Reader.BlobTable`,
  `Reader.ImageMetadata`, and the path-based `Reader.ReadFile` all just work on
  compressed WIMs and ESD-adjacent files.
- `EncodeResourceData` is the write-side counterpart: given a resource's full
  uncompressed bytes, a compression type, and a chunk size, it produces the
  correctly-framed on-disk payload (falling back to raw storage per chunk, or
  for the whole resource, exactly as wimlib's own writer does) plus the
  `ResFlag*` bits for that resource's `ResourceHeader`.

**Solid resources remain out of scope.** A solid resource
(`ResourceHeader.IsSolid`, `ResFlagSolid`) packs multiple blobs into one shared
compressed stream; unpacking that is a separate, larger piece of
container-level complexity than per-resource chunk framing (see
`BlobTable.SolidResourceRun`, which models the parse-level structure of a solid
run without unpacking it). Reading or writing a solid resource as data returns
`ErrCompressedResource`.

**Assembling a complete, multi-image WIM file is directly supported** via
`WriteTo`/`Assemble`. Given one `*ImageMetadata` per image, a `*BlobTable`
whose entries already have correct `Hash`/`RefCount`/`PartNumber` (computing
reference counts by walking dentry trees is the caller's job, not the
writer's -- exactly as it already is for `driver.Install`), a `*XMLData`, and
a `BlobSource` supplying each blob's raw content by hash, the writer lays out
the whole file (blobs, then one metadata resource per image, then the blob
table, then XML data, with the header patched in last once every offset is
known -- the same order `write_test.go`'s original test-only `buildMinimalWIM`
helper already used, generalized here into real package API), fills in every
`BlobDescriptor.Resource`, and sets `Header.ImageCount`/`BootIndex`/
`BootMetadata` correctly. It supports all three compression types (or none)
uniformly for the whole container (a WIM has one compression type, not a
per-blob choice), and multiple images sharing a blob by hash (the caller
supplies one blob-table entry with `RefCount` reflecting all the images that
reference it; the writer places its content once).

Out of scope, matching the rest of this package: solid resources (the writer
never emits `ResFlagSolid`) and multi-part/split WIMs (`PartNumber`/
`TotalParts` are always written as 1/1). Filesystem capture/apply remains out
of scope too. See `writer.go`'s doc comments and `writer_test.go` for the
full verification story.

**An integrity table can be computed and appended**, mirroring DISM's
`/CheckIntegrity`: set `WriteOptions.ComputeIntegrityTable` and `WriteTo`/
`Assemble` compute one in the same single pass as everything else (not a
separate post-process re-read of the output file) and set
`Header.IntegrityTable` accordingly. The exact byte range and chunking this
covers were confirmed empirically (2026-07-10) against two real WIMs with
integrity tables (a Windows 11 23H2 `boot.wim` and `install.esd`, both
showing "Integrity info" in `wimlib-imagex info`'s Attributes): by reading
each file's real `IntegrityTable` with this package's own `Reader` and
independently recomputing SHA-1 hashes over several candidate byte-range
hypotheses, only one matched exactly -- `[HeaderSize, offset of the blob
table + size of the blob table)`, i.e. from byte 208 through the end of the
blob table resource, **excluding** the XML data and the integrity table
itself, even though the integrity table is physically appended to the file
*after* the XML data. This is corroborated by wimlib's own source
(`src/integrity.c`'s `calculate_integrity_table`/`write_integrity_table` and
`src/write.c`'s `finish_write`), which computes the table over exactly
`[WIM_HEADER_DISK_SIZE, new_blob_table_end)`. See `integrity_write.go`'s doc
comment for the full evidence and `IntegrityTable`'s (in `integrity.go`)
updated doc comment for `ParseIntegrityTable`'s `numCheckedBytes`. A
convenience read-side check, `Reader.VerifyIntegrity`, recomputes and
compares against a WIM's stored table using this same convention; it returns
`ErrNoIntegrityTable` if the WIM has none.

**Exporting a subset of a WIM's images into a new, standalone file is
directly supported** via `ExportImage`/`ExportImageAssemble`, mirroring
DISM's `/Export-Image`. Given a source `*Reader` plus its already-parsed
`*BlobTable`/`*XMLData` and a list of 1-based source image indices (in the
order they should appear in the output; more than one is supported, not just
the single-image case DISM is usually invoked for), `ExportImage`:

- Reads each selected image's metadata resource via
  `srcBlobTable.MetadataResources()[index-1]` (source image index *i*'s
  metadata resource is entry *i-1* of that slice -- the same
  order-correspondence convention `WriteTo` itself establishes when writing
  images, confirmed self-consistently by this package's own writer/reader
  round-trip tests; real multi-image WIMs to cross-check against externally
  were not available in this environment, since the local sample
  `boot.wim`/`install.esd` each contain only one image).
- Walks each selected image's entire directory-entry tree (all descendants,
  all streams, unnamed and named/alternate alike) and counts blob references
  *across only the images being exported* -- not the source file's original,
  possibly wider-scope `RefCount`s -- building a fresh `*BlobTable` containing
  only the hashes actually used, with correct `RefCount`s and `PartNumber: 1`.
- Builds new `XMLData` containing only the selected images' `<IMAGE>`
  elements, renumbered sequentially from `INDEX="1"`, preserving each
  element's full original inner content verbatim via the
  `encoding/xml` `innerxml`-struct-tag technique (so real detail like
  `<WINDOWS><EDITIONID>` survives, not just the fields `XMLImage` models).
  Any `<WIM>`-level-only content beyond the `<IMAGE>` elements themselves is
  dropped (not preserved or recomputed) -- in practice, real WIM XML data has
  none, but the format doesn't forbid it.
- Streams every referenced blob's content lazily straight out of the source
  WIM via `NewReaderBlobSource` (a small, general-purpose `BlobSource`
  adapter over a `Reader`+`BlobTable`, independently useful beyond
  `ExportImage`), then calls `WriteTo`/`Assemble` with the caller's
  `WriteOptions` -- which may specify a different `CompressionType`/
  `ChunkSize` than the source used, transparently supporting recompression,
  since `WriteTo` always re-encodes every blob's raw bytes regardless of how
  the source stored them.

`Reader.ReadResource` is the small exported wrapper (over the previously
unexported decoding logic `Reader` already used internally) that
`NewReaderBlobSource` is built on, for callers that want to decompress an
arbitrary resource given its header directly.

The path-based tree operations have their own explicit scope boundaries:
`DirEntry.Add` takes a `Hash`, not raw content bytes - getting those bytes into
a `BlobTable`/output file is the caller's job, exactly as `driver.Install`
already does by returning `[]NewBlob` for the caller to place - and
`DirEntry.Remove` does not adjust any `BlobTable`'s reference counts for
streams a removed subtree stops referencing; a caller that cares can walk the
removed subtree's `Streams` itself.

## Layout

Everything lives in a single package, `wim`, one file per format concern:

| File | Responsibility |
|------|----------------|
| `wim.go` | package doc, `Hash`, `GUID`, shared helpers |
| `resource.go` | `ResourceHeader` (the 7-byte-size + flags reshdr) |
| `header.go` | `Header` (208-byte WIM header) + flags/versions |
| `blobtable.go` | `BlobTable`, `BlobDescriptor`, solid-run grouping |
| `security.go` | `SecurityData` (SD table) |
| `dentry.go` | `DirEntry` tree parsing |
| `dentry_write.go` | `DirEntry` tree serialization + subdir-offset layout |
| `metadata.go` | `ImageMetadata` (security data + dentry tree) |
| `xml.go` | `XMLData` (UTF-16LE + BOM, light `IMAGE`/`WINDOWS` parsing) |
| `integrity.go` | `IntegrityTable` |
| `encoding.go` | UTF-16LE helpers |
| `chunk.go` | `CompressionType`, `Header.CompressionType`, chunk-table size/count helpers shared by encode and decode |
| `decompress.go` | `DecodeResourceData`, codec dispatch for decoding |
| `compress.go` | `EncodeResourceData`, codec dispatch for encoding |
| `writer.go` | `WriteTo`/`Assemble`: full multi-image WIM assembly, `BlobSource`/`MapBlobSource`, `WriteOptions` |
| `integrity_write.go` | integrity-table computation for `WriteTo` (`integrityAccumulator`), `Reader.VerifyIntegrity` |
| `reader.go` | `Reader` over `io.ReaderAt`, incl. `Reader.ReadResource` |
| `export.go` | `NewReaderBlobSource`, `ExportImage`/`ExportImageAssemble` |
| `path.go` | `ErrNotFound`, `DirEntry.Child`, `DirEntry.Lookup` |
| `read.go` | `Reader.ReadFile` |
| `tree.go` | `DirEntry.Add` |
| `remove.go` | `DirEntry.Remove` |
| `rename.go` | `DirEntry.Rename`, `DirEntry.ReadDir` |
| `glob.go` | `MatchName` (DOS-style name globbing) |
| `wim_test.go` | round-trip tests for every component |
| `path_test.go` | tests for the path-based tree operations and `MatchName` |
| `chunk_test.go` | synthetic round-trip tests for `EncodeResourceData`/`DecodeResourceData` and `Header.CompressionType` |
| `testdata_test.go`, `testdata/*.bin` | real, multi-chunk compressed resource fixtures (LZX from a real Windows 11 boot.wim, XPRESS/LZMS from fresh `wimlib-imagex capture` output) for hermetic ground-truth decode tests |
| `write_test.go` | hand-assembles a full minimal, single-image WIM using `EncodeResourceData` plus this package's own serialization primitives directly (the from-scratch reference `writer.go`'s `WriteTo` generalizes), then reads it back with `Reader` |
| `writer_test.go` | multi-image, deduplicated-blob, all-compression-type (plus uncompressed) tests of `WriteTo`/`Assemble`, read back with `Reader` |
| `integrity_write_test.go` | builds a WIM with `ComputeIntegrityTable` set, independently recomputes every stored hash directly over the raw output bytes, confirms `Reader.VerifyIntegrity` agrees and correctly rejects corruption inside (but not outside) the checked range, and confirms no integrity table is written by default |
| `export_test.go` | `ExportImage`/`ExportImageAssemble`: single-image and multi-image-with-reordering exports off a hand-built 3-image WIM with a blob shared between two of the images, confirming only the exported images/blobs survive, `RefCount`s reflect only the exported images' usage, XML per-image content (including a `<WINDOWS><EDITIONID>` beyond `XMLImage`'s modeled fields) survives renumbering, and recompression to a different `CompressionType` during export |

## Usage

```go
f, _ := os.Open("install.wim")
defer f.Close()
fi, _ := f.Stat()

r, err := wim.NewReader(f, fi.Size())
if err != nil {
    log.Fatal(err)
}

hdr := r.Header()
fmt.Printf("version=%#x images=%d\n", hdr.Version, hdr.ImageCount)

// XML data is uncompressed, so it can always be read back.
x, err := r.XMLData()
if err != nil {
    log.Fatal(err)
}
for _, im := range x.Images {
    fmt.Printf("image %d: %s\n", im.Index, im.Name)
    if im.Windows != nil {
        // Windows-family images (e.g. install.wim/install.esd) additionally
        // carry a <WINDOWS> sub-element with the fields DISM's
        // `/Get-WimInfo` surfaces (Architecture, Edition, Installation,
        // Language, Version). Confirmed against a real Windows 11 23H2
        // install.esd's XML data, 2026-07-10.
        fmt.Printf("  arch=%s edition=%s languages=%v\n",
            im.Windows.ArchitectureName(), im.Windows.EditionID, im.Windows.Languages)
    }
}

// The blob table transparently decompresses if the WIM is compressed.
bt, err := r.BlobTable()
if err != nil {
    log.Fatal(err)
}
for _, res := range bt.MetadataResources() {
    if res.IsSolid() {
        continue // solid resources remain out of scope
    }
    md, err := r.ImageMetadata(res) // works whether or not res is compressed
    if err != nil {
        log.Fatal(err)
    }
    _ = md.Root // walk the directory tree
}
```

Writing a compressed resource's payload directly (for a caller assembling its
own WIM file, one resource at a time):

```go
payload, flags, err := wim.EncodeResourceData(fileBytes, wim.HdrFlagCompressLZX, hdr.ChunkSize)
if err != nil {
    log.Fatal(err)
}
resHdr := wim.ResourceHeader{
    SizeInWIM:        uint64(len(payload)),
    Flags:            flags, // 0 or ResFlagCompressed, decided for you
    OffsetInWIM:      offset, // wherever the caller places it in the file
    UncompressedSize: uint64(len(fileBytes)),
}
```

Each component also parses from and serializes to a byte slice directly
(`Parse*` functions and `AppendTo` methods), so you can build a WIM structure in
memory and emit the individual resources.

Assembling a complete two-image, LZX-compressed WIM from scratch:

```go
images := []*wim.ImageMetadata{image1, image2} // built via DirEntry.Add etc.
bt := &wim.BlobTable{ /* Hash/RefCount/PartNumber already correct */ }
xml := &wim.XMLData{Document: `<WIM><IMAGE INDEX="1">...</IMAGE><IMAGE INDEX="2">...</IMAGE></WIM>`}
blobs := wim.MapBlobSource{ /* hash -> raw content bytes, for every blob bt references */ }

out, err := wim.Assemble(images, bt, xml, blobs, wim.WriteOptions{
    CompressionType: wim.HdrFlagCompressLZX,
    ChunkSize:       32768,
    BootIndex:       1, // 1-based index into images, or 0 for no boot image
})
if err != nil {
    log.Fatal(err)
}
os.WriteFile("out.wim", out, 0644)
```

For large WIMs, prefer `wim.WriteTo(f, images, bt, xml, blobs, opts)` writing
directly to an `*os.File` (or any `io.WriteSeeker`) over `Assemble`, and
implement `BlobSource` to stream each blob's bytes from wherever they
actually live, rather than holding every blob's content in memory at once.

Adding an integrity table (DISM's `/CheckIntegrity`) to any of the above is
just one more `WriteOptions` field:

```go
out, err := wim.Assemble(images, bt, xml, blobs, wim.WriteOptions{
    CompressionType:       wim.HdrFlagCompressLZX,
    ChunkSize:             32768,
    ComputeIntegrityTable: true, // computed in the same pass, no extra re-read
})
```

Exporting a subset of an existing WIM's images into a new file (DISM's
`/Export-Image`), optionally recompressing:

```go
f, _ := os.Open("install.wim")
defer f.Close()
fi, _ := f.Stat()
r, err := wim.NewReader(f, fi.Size())
if err != nil {
    log.Fatal(err)
}
bt, err := r.BlobTable()
if err != nil {
    log.Fatal(err)
}
xmlData, err := r.XMLData()
if err != nil {
    log.Fatal(err)
}

out, err := os.Create("exported.wim")
if err != nil {
    log.Fatal(err)
}
defer out.Close()

// Export source image 3 alone, recompressing from the source's compression
// to XPRESS.
_, err = wim.ExportImage(r, bt, xmlData, []int{3}, out, wim.WriteOptions{
    CompressionType: wim.HdrFlagCompressXPRESS,
    ChunkSize:       32768,
})
if err != nil {
    log.Fatal(err)
}
```

## Tests

```
go test ./...
```

The tests assert byte-exact round trips (parse ∘ serialize = identity) for the
header, resource header, blob table, security data, integrity table, XML data,
the directory-entry tree (including named alternate data streams), and the full
image metadata resource.

Compression-related tests (`chunk_test.go`, `testdata_test.go`, `write_test.go`)
cover:

- Synthetic round trips through `EncodeResourceData`/`DecodeResourceData` for
  all three compression types, across chunk-boundary edge cases (data under
  one chunk, exactly one chunk, several full chunks, a partial final chunk,
  and data that doesn't compress at all), plus `Header.CompressionType`'s
  flag-resolution rules (including `HdrFlagCompressXPRESS2` mapping to plain
  XPRESS, per wimlib's own `src/wim.c`).
- Real-data ground truth (`TestDecodeResourceDataRealWIMGroundTruth`):
  hermetic, embedded (`go:embed`, binary `testdata/*.bin` fixtures, never
  base64/text-encoded) multi-chunk compressed resources -- one real
  Windows-produced LZX resource from `sources/boot.wim` of a Windows 11 23H2
  install image, plus fresh XPRESS and LZMS resources from `wimlib-imagex
  capture` output -- decoded and checked against known-correct SHA-1 hashes.
- Full write-then-read verification (`TestEncodeResourceDataFullWIMRoundTrip`):
  hand-assembles a complete minimal WIM using `EncodeResourceData` plus this
  package's own `Header`/`BlobTable`/`ImageMetadata`/`DirEntry`/`XMLData`
  primitives, for all three compression types, then reads it back with this
  package's own `Reader` and confirms every file's contents match.
- **Verified independently against `wimlib-imagex extract`** (a real,
  external, independent decoder) during development, for all three
  compression types, using the same hand-assembled-WIM construction as
  `TestEncodeResourceDataFullWIMRoundTrip`: text, highly repetitive data,
  random/incompressible data, a real x86 executable spanning multiple chunks,
  and a 1-byte file. All three compression types extracted byte-for-byte
  identical to the original files. This external-tool verification is not
  re-run at `go test` time (this package has no test-time dependency on
  `wimlib-imagex`), so it is recorded here as a one-time result, following the
  same convention already used by the sibling `xpress`/`lzx`/`lzms` packages'
  own READMEs.

  This verification round caught and fixed a real bug in the sibling `lzx`
  package: when exactly one symbol in a Huffman alphabet had nonzero
  frequency, its encoder produced an incomplete prefix code that wimlib's
  decoder correctly rejects (`wimlib-imagex extract` failed with "The WIM
  contains invalid compressed data"), even though `lzx`'s own decoder
  accepted it. See `lzx/README.md`'s "A bug found and fixed by this
  verification" section and `lzx/huffman.go` for the fix.

- **`WriteTo`/`Assemble` verified independently against `wimlib-imagex`**
  (2026-07-10), for a genuinely multi-image case (not just the single-image
  construction above): a hand-built 2-image WIM (files of several sizes
  including one spanning multiple chunks, plus one blob shared/deduplicated
  between both images via a single blob-table entry with `RefCount` 2) was
  assembled once each for `CompressionNone`, XPRESS, LZX, and LZMS, then
  checked with:
  - `wimlib-imagex info`: correctly reports `Image Count: 2`, both images'
    names, the right `Compression`/`Version`/`Chunk Size`/`Boot Index` for
    every case.
  - `wimlib-imagex apply` (both images, all four cases): every extracted
    file's SHA-1 matched the original content exactly, including the shared
    file extracted from *both* images.

  This is a from-scratch construction (not reusing wimlib output), so it also
  independently confirms this package's own real-WIM findings used to build
  `WriteTo`: reading `boot.wim`/`install.esd` from a real Windows 11 23H2
  install image with this package's own `Reader` showed the blob table and
  XML data resources stored with `ResourceHeader.Flags == ResFlagMetadata`
  only (never `ResFlagCompressed`, i.e. uncompressed) while each image's
  metadata resource carries `ResFlagMetadata|ResFlagCompressed` (compressed,
  non-solid, even in the LZMS-compressed `install.esd`); and that `boot.wim`
  (LZX) uses `Version 0x10d00` (`VersionDefault`) while `install.esd` (LZMS)
  uses `Version 0xe00` (`VersionSolid`). `WriteTo` reproduces both
  conventions exactly. As with the single-image verification above, this
  external-tool check is not re-run at `go test` time; it is recorded here as
  a one-time result.

- **`ExportImage` verified against real file content and `wimlib-imagex`**
  (2026-07-10): a small (well under 1 MB total), hand-built 2-image WIM was
  assembled (LZX, mirroring `boot.wim`'s real compression) from a handful of
  real files pulled out of a mounted Windows 11 23H2 install image
  (`appcompat.xsl`, ~11 KB; `arunimg.dll`, ~150 KB, spanning multiple 32 KiB
  chunks; `bcd.dll`, ~150 KB, shared between both images; `acres.dll`,
  ~345 KB), with a distinctive `<DESCRIPTION>`/`<NAME>` per image. Image 1 was
  then exported via `ExportImage` with `ComputeIntegrityTable` set and
  recompressed from LZX to XPRESS. The result:
  - `wimlib-imagex info` reported `Image Count: 1`, `Compression: XPRESS`,
    `Attributes: Integrity info`, and the correct `Name`/`Description` for
    the exported image under `Boot Index: 1`.
  - `wimlib-imagex extract --to-stdout` for each of `appcompat.xsl`,
    `arunimg.dll`, and `bcd.dll` was byte-for-byte identical to the original
    files on disk.
  - `wimlib-imagex verify` reported the file "successfully verified" (an
    independent, external confirmation that the computed integrity table is
    correct, on top of this package's own `Reader.VerifyIntegrity`).
  - This package's own `Reader` confirmed `bcd.dll` (shared by both source
    images, but only one of them exported) has `RefCount == 1` in the
    exported blob table, not the source's 2.

  A full-scale run against the entire real `boot.wim` (multi-gigabyte,
  ~21,000 files) was deliberately not attempted for this verification: this
  package's LZX/XPRESS/LZMS codecs are simple, unoptimized pure-Go
  implementations not tuned for multi-GB throughput, and a small real-file
  sample proves the same recompression/export correctness far faster. As
  with the other external-tool checks above, this is recorded as a one-time
  result, not re-run at `go test` time.

## License

MIT OR Apache-2.0.
