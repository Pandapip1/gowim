# gowim/component

A queryable model of an offline Windows image's servicing component store:
ties together the sibling `mum` package's parsed manifests -- both
package-level `.mum` files and, once decompressed by the sibling `pa30`
package, component-level WinSxS `.manifest` files -- into a single index
searchable by name pattern, KB identifier, or processor architecture, with
package/component dependency edges resolvable against each other.

This is the "actual Windows component module" referenced throughout this
repo's top-level `TODO.md`. It does not itself parse XML or decompress
PA30 (see `mum` and `pa30`) -- it only builds a read-only, in-memory view
over already-parsed manifests, plus (see `Remove` below) a best-effort way
to delete one's files from an image.

## Scope

- Parses every `.mum` file it's given via `mum.Parse` directly (no PA30
  involved -- `.mum` files are plain XML).
- Parses every `.manifest` file it's given: if it has the 8-byte `"DCM"`
  +version prefix, decodes it via the sibling `pa30` package's
  `DecodeWithSource` (which needs the real shared dictionary described in
  `pa30/README.md`) first; otherwise treats it as already-plain XML (some
  real files, e.g. the VC++ 8.0/9.0 CRT's, are stored uncompressed -- see
  `pa30/README.md`'s SRC/FULLSRC section). Either way, `mum.Parse` runs on
  the resulting XML. Confirmed (2026-07-13) to succeed on all 17189 files
  in a real image's `Windows\WinSxS\Manifests`.
- Records per-file failures on that file's `Entry.Err` rather than
  aborting a whole build, so a `Store` can still be built from the rest of
  a real image's files even if a future edge case isn't covered.
- `BuildFromImage` reads and parses every file concurrently over a
  `GOMAXPROCS`-bounded worker pool (`runJobsParallel`): each file's
  read+parse is independent of every other's, and result order never
  mattered (`Build` only indexes entries into a map). Safe because
  `*wim.Reader.ReadFile` goes through `ReadAt`, concurrency-safe the same
  way `os.File.ReadAt` is. Worth doing given a real image's
  `servicing\Packages` + `WinSxS\Manifests` together number in the tens of
  thousands of files; see TODO.md's "Performance: concurrency
  opportunities" entry. Verified with `go test -race`.
- Dependency-edge resolution only follows `AssemblyIdentity`-based
  references (`Package.Parent`, `Update.Package`/`Update.Component`,
  `Dependency.DependentAssembly`) -- it does not follow
  `DeclareCapability`-based capability tokens (a different identity
  namespace), which aren't modeled as dependency edges here.
- `Remove` deletes an `Entry`'s on-disk files as a best-effort fallback,
  per this repo's TODO.md CBS/servicing research verdict: the `COMPONENTS`
  hive's schema is undocumented and not safely mutable, so `Remove` only
  ever deletes plain files/directories (a `KindPackage` entry's `.mum` +
  paired `.cat`; a `KindComponent` entry's `.manifest` + optional WinSxS
  payload directory), leaving that hive untouched and, by design,
  inconsistent with the image afterward -- a documented, permanent
  limitation. Both pairing assumptions (`.mum`/`.cat` always paired 1:1;
  a `.manifest` file's WinSxS payload directory being optional) were
  verified against a real Windows 11 23H2 image, not assumed -- see
  `remove.go`'s doc comment.
- `Install`/`InstallRegistry` are the reverse direction: given a component's
  plain-XML `.manifest`, its payload files, and optionally a package's
  `.mum`/`.cat`, they place the files into an image's directory tree and
  write the servicing bookkeeping into the `COMPONENTS` and `SOFTWARE`
  hives. See "Installation" below.

## Installation

`Install` mirrors the sibling `driver` package's `Install`: it mutates an
image's in-memory directory-entry tree and blob table and returns the new
blobs the caller must place when it eventually writes the WIM. As there,
registry work is a second function (`InstallRegistry`) because the hives are
loaded and saved separately (see the sibling `registry` package).

### Build-once vs serviceable

`Installation.Serviceability` has no usable zero value; the caller must pick
one, and the two are genuinely different:

| | `BuildOnce` | `Serviceable` |
|---|---|---|
| files placed | yes | yes |
| `COMPONENTS`/`SOFTWARE` hive entries | **no** | yes, via `InstallRegistry` |
| component works at runtime | yes | yes |
| image survives a later `DISM /ScanHealth` or update | **no** | intended to |

Nothing at *runtime* reads the `COMPONENTS` hive -- measured, not assumed:
`ntoskrnl.exe`, `smss.exe`, `csrss.exe`, `winsrv.dll`, `kernel32.dll`,
`drvstore.dll`, `ntdll.dll`, `sxs.dll` and `sxsstore.dll` in a real image
contain zero references to `\Registry\Machine\COMPONENTS`; only the
servicing stack's `wcp.dll` does. So a `BuildOnce` install produces a
working component.

But an entry-less component is not merely invisible to servicing: CheckSUR
and `DISM /ScanHealth` emit a named finding for exactly this condition,
`CSI Missing Winning Component Key`, quoting the WinSxS keyform, and there
is a reported case of updates failing until the missing
`DerivedData\Components` keys were restored. `InstallRegistry` therefore
*refuses* to run for a `BuildOnce` installation (`ErrBuildOnce`) rather than
letting the choice be made by accident. Full evidence, with grading, is in
`TODO.md`'s "Component-installation research pass".

### Manifests are written as plain XML, and that is fine

No PA30 encoder is needed. `wcp.dll`'s `GetCompressedFileType` classifies a
manifest purely from its own first four bytes and `DecompressManifest`
treats "not compressed" as a success path that returns the buffer untouched;
401 of the 28069 manifests in a real image are already plain. `Install`
rejects a `DCM`-prefixed manifest rather than writing one.

### What the caller has to supply, and why

CBS embeds a 16-hex-digit hash of the assembly identity in WinSxS keyforms,
deployment key names, `SideBySide\Winners` key names, and truncated
`f!`/`p!`/`s!`/`i!` value names. That hash is undocumented and gowim cannot
compute it -- the obvious candidates (MD5/SHA-1/SHA-256/SHA-512 of the
identity in ASCII or UTF-16LE, terminated or not, either case, first or last
8 bytes, either byte order) were tried against real values and none matches.
So `ComponentInstall.KeyForm`, `DeploymentInstall.KeyName` and
`WinnersInstall.KeyForm` are supplied whole by the caller, and a payload file
name longer than 25 characters is a hard error rather than a guess.

### Known gaps, stated rather than hidden

- **`WinSxS\FileMaps\*.cdf-ms` is not updated.** 3764 binary
  per-destination-directory index files (magic `PcmH`) whose format was not
  reverse-engineered, and whose staleness is not known to break anything and
  not known not to. See `filemaps.go`, `ErrFileMapsNotUpdated`, and
  `InstallationTouchesFileMaps` for whether the gap is even relevant to a
  given installation.
- **`p!`/`s!`/`i!` deployment-to-package links are not written**, because
  their value names embed the uncomputable hash above.
- **A third-party `.cat` never chains to a Microsoft root**, so the component
  can never be validated by CBS regardless of anything written here.
- **No live confirmation.** Nothing here has been proven by installing a
  component into a running Windows. Every claim above is from offline
  measurement, disassembly, or documentation.
- `Install` and `Remove` are not exact inverses: `Remove` works from a
  parsed `Entry` and so removes the manifest, the WinSxS payload directory,
  and a package's `.mum`+`.cat`, but not the `WinSxS\Catalogs` copy nor
  payload projected into `System32` and friends. Both asymmetries are
  asserted in the tests so they stay visible.

## Usage

```go
// Installation: files, then (for a serviceable image) the hives.
inst := &component.Installation{
    Serviceability: component.Serviceable, // or component.BuildOnce
    Components: []component.ComponentInstall{{
        KeyForm:  "amd64_contoso.widget_0123456789abcdef_1.0.0.0_none_fedcba9876543210",
        Manifest: plainXMLManifestBytes,
        Files: []component.PayloadFile{{
            Name:     "widget.dll",
            Data:     widgetDLL,
            DestDirs: []string{`Windows\System32`},
        }},
    }},
}
root, newBlobs, err := component.Install(imageMetadata, blobTable, inst)
// ... then, for Serviceable only:
err = component.InstallRegistry(&component.Hives{
    Components: componentsHive.Hive.Root, // from registry.LoadHiveSet
    Software:   softwareHive.Hive.Root,
}, inst)
```

```go
// From raw bytes directly (e.g. already extracted from an image):
mumEntry := component.ParseMUM("KB5030219.mum", mumBytes)
manifestEntry := component.ParseManifest("amd64_....manifest", rawManifestBytes, dictionaryBytes)

store := component.Build([]*component.Entry{mumEntry, manifestEntry})

// Or directly from a WIM image's root directory tree (see the sibling `wim`
// package for Reader/DirEntry/BlobTable):
store, err := component.BuildFromImage(reader, root, blobTable, dictionaryBytes)

// Query:
store.ByName("HyperV-*")            // glob over identity names
store.ByKB("KB5030219")             // exact Package.Identifier match
store.ByArchitecture("amd64")
store.ResolveDependencies(someEntry) // look up each declared dependency in the store

// Removal: resolve a pattern via the Store, then delete each match's files.
for _, e := range store.ByName("Some-Bloat-Package*") {
    if err := component.Remove(root, blobTable, e); err != nil {
        log.Fatal(err)
    }
}
```

## Tests

```
go test ./...

# Additionally validate the installation schema against a real image:
GOWIM_TEST_IMAGE=/path/to/install.wim go test -run TestRealImage -v ./...
```

The installation schema is entirely undocumented, so the tests that matter
most for it run against a real, unmodified Windows `install.wim` rather than
a fixture (`install_realimage_test.go`, skipped unless `GOWIM_TEST_IMAGE` is
set; `GOWIM_TEST_IMAGE_INDEX` selects an image other than the first; expect
about ten minutes, most of it decompressing all 28069 manifest resources
twice). They re-derive, as assertions, the same measurements the doc
comments cite:

- `WinSxS\Manifests\*.manifest` is 1:1 by name with
  `COMPONENTS\DerivedData\Components`.
- `S256H` is the SHA-256 of the manifest's *content* -- of the raw bytes for
  every plain manifest (the shape `Install` writes), and of the
  PA30-decompressed XML for the rest.
- `CanonicalIdentity` reproduces the hive's `identity` value for every
  component in the image, including the one identity field CBS does *not*
  copy through verbatim: real manifests spell `versionScope` three ways
  (`nonSxS`, `nonSXS`, `nonSxs`) and the hive always spells it `NonSxS`.
  That normalization was found by this test failing, not by reading
  anything.
- `f!` value names are verbatim only up to the boundary `fileValueName`
  enforces.
- `DeploymentKeyNamePrefix` reproduces the computable field of every
  deployment key name, checked against that key's own `appid`.
- `WinSxS\Catalogs\<hex>.cat` is 1:1 with `CanonicalData\Catalogs\<hex>` and
  the name really is `CatalogThumbprint` of the file.
- And an end-to-end round trip: install a component into the real image's
  real directory tree and real `COMPONENTS` hive, check the resulting key's
  value names and types against what real components in that same hive use,
  then `Remove` it and check the tree is back. The image file itself is
  opened read-only and never written.

`testdata/` also holds one real plain-XML manifest
(`plain_common_controls.manifest`, taken verbatim from that image) whose
`S256H` and canonical identity are asserted against the values that image's
own hive records, so the core claims stay covered without the 7.5 GB image.
The rest of `testdata/` holds real files copied from the sibling `mum`/`pa30` packages'
own testdata (see their READMEs for full provenance): two real `.mum`
files, one real `.manifest` file plus the real shared dictionary needed to
decode it. Tests cover parsing both file kinds, dependency-edge extraction,
and every `Store` query method, including exercising the "dependency not
found in this Store" path (expected when a `Store` is built from a small
subset of a real image's files -- e.g. this package's own test fixtures --
even though a full real image now decodes essentially completely; see
Scope above).

## License

MIT OR Apache-2.0.
