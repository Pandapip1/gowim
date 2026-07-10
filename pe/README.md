# gowim/pe

A Go implementation of the **container structure** of the PE/COFF (Portable
Executable / Common Object File Format) used by Windows binaries, including
`.sys` kernel driver files. Cross-checked against the "Microsoft Portable
Executable and Common Object File Format Specification" on Microsoft Learn
(<https://learn.microsoft.com/en-us/windows/win32/debug/pe-format>).

This package exists to support installing `.inf`/`.cat`/`.sys` driver
packages into WIM disk images: it lets a caller validate that a `.sys` file
is a well-formed PE image, read its machine/architecture/timestamp/checksum,
and — most importantly — locate the Attribute Certificate Table (the
embedded Authenticode signature) as a raw byte range, ready to hand to the
sibling package `github.com/gavin-john/gowim/cat`, which structurally parses
the PKCS#7 SignedData blob inside it.

## Scope

This package handles the **structure** of a PE image. It reads and writes:

- the MS-DOS header (`IMAGE_DOS_HEADER`) and PE signature — the DOS stub
  program between them is preserved verbatim, not disassembled or regenerated
- the COFF file header (`IMAGE_FILE_HEADER`)
- the optional header, both the PE32 (`IMAGE_OPTIONAL_HEADER32`) and PE32+
  (`IMAGE_OPTIONAL_HEADER64`) variants, and its data directory array
- section headers (`IMAGE_SECTION_HEADER`) and section raw data, exposed as
  opaque byte ranges
- the Attribute Certificate Table: a sequence of `WIN_CERTIFICATE` entries,
  exposed as (revision, type, raw bytes)

It **deliberately does not** implement relocation/import/export/debug-directory
*semantic* parsing, disassembly of code sections, or Authenticode signature
verification. The `bCertificate` payload of a `WIN_CERTIFICATE` entry (a
PKCS#7 SignedData structure) is exposed only as raw bytes; this package does
not import or depend on `cat/`, which does the structural PKCS#7 parsing.

A well-known gotcha this package documents and handles correctly: the
Certificate Table's data directory entry (`DirEntrySecurity`) has a
`VirtualAddress` field that is a **file offset**, not an RVA like every other
directory entry, because attribute certificates are not mapped into memory as
part of the loaded image.

`Parse` followed by `(*Image).AppendTo` reproduces the original bytes exactly
for well-formed inputs (see the round-trip tests).

## Layout

Everything lives in a single package, `pe`, one file per format concern:

| File | Responsibility |
|------|----------------|
| `pe.go` | package doc, shared byte order, error-wrapping helper |
| `dosheader.go` | `IMAGE_DOS_HEADER` (e_magic/e_lfanew) + PE signature, DOS stub passthrough |
| `fileheader.go` | `FileHeader` (`IMAGE_FILE_HEADER`), `Machine*`/`File*` constants |
| `optionalheader.go` | `OptionalHeader` (PE32 and PE32+), `DataDirectory`, `DirEntry*` indices |
| `sectionheader.go` | `SectionHeader`, `Section` (header + raw section bytes) |
| `certtable.go` | `Certificate` (`WIN_CERTIFICATE`), Attribute Certificate Table parsing/serialization |
| `image.go` | `Image` (top-level struct tying everything together), `Parse`/`AppendTo` |
| `pe_test.go` | round-trip tests for hand-built PE32+ and PE32 fixtures |

## Usage

```go
data, err := os.ReadFile("driver.sys")
if err != nil {
    log.Fatal(err)
}

img, err := pe.Parse(data)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("machine=%#04x timestamp=%d checksum=%#x\n",
    img.FileHeader.Machine, img.FileHeader.TimeDateStamp, img.OptionalHeader.CheckSum)

secDir, ok := img.SecurityDirectory()
if !ok || secDir.Size == 0 {
    log.Fatal("driver is not signed")
}

// secDir.VirtualAddress is a file offset here, not an RVA.
signedData := data[secDir.VirtualAddress : secDir.VirtualAddress+secDir.Size]
certs, err := pe.ParseCertificateTable(signedData)
if err != nil {
    log.Fatal(err)
}
for _, c := range certs {
    if c.Type == pe.CertTypePKCSSignedData {
        // c.Data is a raw PKCS#7 SignedData blob (Authenticode signature).
        // Hand it to cat.ParseSignedData (or similar) for structural parsing.
        _ = c.Data
    }
}
```

## Tests

```
go test ./...
```

The tests hand-construct minimal but structurally valid PE32+ and PE32 byte
images (headers, a DOS stub, one section, and — for the PE32+ case — a fake
`WIN_CERTIFICATE` entry referenced by the Security data directory) and assert:

- `Parse` → `AppendTo` round-trips to byte-identical output
- individual header/section/optional-header fields decode correctly
- the fake certificate-table entry can be located and read back as raw bytes
  via the Security data directory offset/size, independent of `Image.Certificates`
- WIN_CERTIFICATE entries pad correctly to 8-byte boundaries

## License

MIT OR Apache-2.0.
