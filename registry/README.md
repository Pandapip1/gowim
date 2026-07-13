# gowim/registry

Ties the sibling [`regf`](../regf) package (registry hive file format) to
the sibling [`wim`](../wim) package (offline image file access): locates and
parses the standard set of registry hives inside an offline Windows image,
and writes a modified hive's bytes back into the image's directory-entry
tree and blob table. This is the "registry generalization" work referenced
in the top-level `TODO.md`.

It does not itself parse the regf format or the WIM format -- see `regf` and
`wim` for that -- and it does not write the final output WIM file: like the
sibling [`driver`](../driver) package's `Install`/`Uninstall`,
`LoadHiveSet`/`Hive.Save` only produce/mutate in-memory structures (a
`*regf.Hive` tree, a `*wim.DirEntry`'s stream hash, a `*wim.BlobTable`'s
entries) plus any newly-produced blob bytes the caller must place when it
eventually assembles/writes the WIM file.

## Scope

- `LoadHiveSet` locates and parses every standard hive present under an
  image's root directory entry: `SYSTEM`, `SOFTWARE`, `DEFAULT`, `SAM`,
  `COMPONENTS` (all under `Windows\System32\config\`), and
  `Users\Default\NTUSER.DAT`. A hive that does not exist in a given image
  (e.g. a WinPE image with no `SAM`/`COMPONENTS` hive) is simply omitted
  from the result rather than causing an error.
- `Hive.Save` re-serializes a (presumably caller-modified) hive tree,
  updates its `*wim.DirEntry`'s content hash, and extends/adjusts the
  `*wim.BlobTable` -- deduplicating by hash and incrementing an existing
  entry's `RefCount` exactly like `driver.Install`, and decrementing the
  hive's previous hash's `RefCount` exactly like `driver.Uninstall` (never
  letting it underflow past 0, never reclaiming a zero-`RefCount` entry
  itself -- that's a whole-WIM-aware concern for a higher-level caller).
- Registry navigation/mutation itself (finding a subkey, setting a value,
  deleting a subtree, by name or by full backslash-separated path) is not
  this package's concern -- see the sibling `regf` package's generic
  `Key`/`Value` methods (`Subkey`, `FindOrCreateSubkey`, `DeleteSubkey`,
  `Value`, `SetValue`, `DeleteValue`, `OpenPath`, `FindOrCreatePath`,
  `DeletePath`), which work against any hive's tree once `LoadHiveSet` has
  it loaded.

## Usage

```go
r, err := wim.NewReader(imageFile, size)
bt, _ := r.BlobTable()
meta, _ := r.ImageMetadata(bt.MetadataResources()[0])

hs, err := registry.LoadHiveSet(r, meta.Root, bt)
if err != nil {
    log.Fatal(err)
}

system := hs.Hives[registry.HiveSystem]
currentControlSet, err := service.CurrentControlSet(system.Hive.Root)
servicesKey := currentControlSet.FindOrCreateSubkey("Services")
service.Disable(servicesKey, "SomeBloatService")

newBlob, err := system.Save(bt) // updates system.Entry + bt in place
if newBlob.Data != nil {
    // place newBlob.Data at newBlob.Hash when assembling the output WIM
}
```

## Tests

```
go test ./...
```

Tests build a real WIM image in memory (via the sibling `wim` package's own
`Assemble`/`NewReader`, mirroring `wim`'s own `writer_test.go` fixture
approach, not a hand-rolled stand-in) containing one or more small,
structurally valid hives (built via `regf.Hive.AppendTo` from Go struct
literals, mirroring `regf`'s own `TestBuildFromStructLiterals` fixture
shape), and cover: `LoadHiveSet` finding only the standard hives actually
present and skipping the rest; `Hive.Save` after a real mutation producing a
new blob, updating the `DirEntry`'s hash, and correctly incrementing/
decrementing `RefCount`s (verified by reparsing the new bytes and by
re-`Parse`-ing the returned blob directly); and `Hive.Save` on an
unmodified hive producing no new blob at all (same hash as before).

## License

MIT OR Apache-2.0.
