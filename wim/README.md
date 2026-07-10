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

It **deliberately does not** implement the LZX / XPRESS / LZMS compression
codecs, nor filesystem capture/apply. Compressed resource payloads are exposed
as raw byte ranges (`Reader.ResourceReader`); reading a compressed resource as
data returns `ErrCompressedResource`. Serialization writes resources
uncompressed.

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
| `xml.go` | `XMLData` (UTF-16LE + BOM, light `IMAGE` parsing) |
| `integrity.go` | `IntegrityTable` |
| `encoding.go` | UTF-16LE helpers |
| `reader.go` | `Reader` over `io.ReaderAt` |
| `wim_test.go` | round-trip tests for every component |

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
}

// The blob table is uncompressed in typical WIMs.
bt, err := r.BlobTable()
if err != nil {
    log.Fatal(err)
}
for _, res := range bt.MetadataResources() {
    if res.IsCompressed() {
        continue // needs a codec, out of scope
    }
    md, err := r.ImageMetadata(res)
    if err != nil {
        log.Fatal(err)
    }
    _ = md.Root // walk the directory tree
}
```

Each component also parses from and serializes to a byte slice directly
(`Parse*` functions and `AppendTo` methods), so you can build a WIM structure in
memory and emit the individual resources.

## Tests

```
go test ./...
```

The tests assert byte-exact round trips (parse ∘ serialize = identity) for the
header, resource header, blob table, security data, integrity table, XML data,
the directory-entry tree (including named alternate data streams), and the full
image metadata resource.

## License

MIT OR Apache-2.0.
