# gowim/inf

A Go package for parsing and serializing **Windows INF files**: the
plain-text section/key/value format used to describe driver installation
packages (`.inf`, typically alongside a `.cat` catalog and one or more
`.sys`/`.dll` payload files). Modeled on Microsoft's published grammar
(Windows Hardware documentation, cross-checked against the "General Syntax
Rules for INF Files", "INF Strings Section", and "INF Version Section"
pages on [Microsoft Learn](https://learn.microsoft.com/windows-hardware/drivers/install/)
as of 2026-07-10).

## Scope

This package handles the **structure** of an INF file: an ordered list of
sections, each an ordered list of key/field entries, faithful enough to
read, edit, and re-serialize a driver INF. It covers:

- section headers (`[Name]`, including quoted names)
- `key = field, field, ...` entries and bare (keyless) directive lines
- `;` end-of-line comments and standalone comment lines
- `\` line continuation
- double-quoted fields (including the `""` and `%%` escapes)
- the `[Strings]` / `[Strings.LanguageID]` token-substitution mechanism
  (`File.Lookup`, `File.Expand`)
- the `[Version]` section fields most relevant to driver installation:
  `Signature`, `Class`, `ClassGuid`, `Provider`, `DriverVer`, `CatalogFile`
  (and the platform-decorated `CatalogFile.ntXXX` entries)
- both non-Unicode (ANSI/OEM/UTF-8) and Unicode (UTF-16LE with a
  byte-order mark) INF file encodings

It **deliberately does not** implement the full INF directive semantic
engine: there is no interpretation of what `AddService`, `CopyFiles`,
`AddReg`, `Include`/`Needs`, or any other directive *means*, beyond exposing
their key and comma-separated fields structurally. Chasing directives
across `[Manufacturer]` / `[Models]` / `[DDInstall]` sections to decide what
a given piece of hardware installs is the job of a higher-level "driver
install" package built on top of this one. It also does not perform
automatic `%token%` substitution during `Parse` (call `Lookup`/`Expand`
explicitly), codepage/DBCS-aware text handling for non-Unicode files
(non-Unicode bytes are treated as an opaque, round-tripped byte string), or
`.cat` catalog verification.

See the package doc in `inf.go` for the precise round-trip contract: what
whitespace and formatting `AppendTo` canonicalizes versus preserves
verbatim.

## Layout

Everything lives in a single package, `inf`, one file per format concern:

| File | Responsibility |
|------|----------------|
| `inf.go` | package doc, `File`, section lookup/merge helpers, `wrapErr` |
| `section.go` | `Section`, `Entry` (the section/key/field model) |
| `encoding.go` | BOM detection, UTF-16LE encode/decode helpers |
| `parser.go` | `ParseFile`: line joining/continuation, comment/quote scanning, entry parsing |
| `writer.go` | `(*File).AppendTo` and the quoting/canonicalization rules |
| `version.go` | `VersionInfo`, `(*File).Version`, `(*File).CatalogFileForPlatform` |
| `strings.go` | `(*File).Lookup`, `(*File).Expand` (`[Strings]` token substitution) |
| `inf_test.go` | round-trip tests |

## Usage

```go
data, _ := os.ReadFile("contoso.inf")

f, err := inf.ParseFile(data)
if err != nil {
    log.Fatal(err)
}

ver := f.Version()
fmt.Printf("class=%s provider=%s catalog=%s\n",
    ver.Class, f.Expand(ver.Provider, ""), ver.CatalogFile)

// Sections are exposed structurally, including repeats.
for _, s := range f.SectionsNamed("Manufacturer") {
    for _, e := range s.Entries {
        if e.HasKey {
            fmt.Println(e.Key, "->", e.Fields)
        }
    }
}

// Build/edit a File in memory and serialize it back out.
f.Sections = append(f.Sections, inf.Section{
    Name: "Strings",
    Entries: []inf.Entry{
        {HasKey: true, Key: "Msft", Fields: []string{"Microsoft"}},
    },
})
out := f.AppendTo(nil)
os.WriteFile("contoso.out.inf", out, 0o644)
```

## Tests

```
go test ./...
```

The tests parse a realistic hand-constructed driver INF (`[Version]`,
`[Manufacturer]`, `[Models]`, `[SourceDisksFiles]`, `[DestinationDirs]`,
`[Strings]`) and assert round-trip fidelity - parse, serialize, re-parse
yields the same structure, and re-serializing already-canonical output is a
fixed point - along with dedicated cases for comments, line continuation,
quoted fields, and a UTF-16LE + BOM encoded file.

## License

MIT OR Apache-2.0.
