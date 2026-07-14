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
- Component/package *installation* (the reverse of `Remove`) is a stated
  future goal but is not implemented -- see `TODO.md`'s "CBS/servicing
  package subsystem" section for what's still open, including whether a
  `pa30` encoder ends up being a prerequisite.

## Usage

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
```

`testdata/` holds real files copied from the sibling `mum`/`pa30` packages'
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
