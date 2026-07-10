# gowim/driver

A Go package that ties together the sibling `wim`, `inf`, `cat`, and `pe`
packages to support installing a Windows driver package (`.inf` + `.cat` +
`.sys` and any other referenced files) into a WIM image's in-memory
directory-entry tree.

Directive semantics are cross-checked against Microsoft's "Windows Hardware /
drivers / install" documentation on
[Microsoft Learn](https://learn.microsoft.com/windows-hardware/drivers/install/)
as of 2026-07-10; see the citations in `driver.go` and `dirid.go`.

## Scope

Given a driver package's files (accessed through an `fs.FS`), `LoadPackage`
parses the `.inf` (via `inf.ParseFile`) and chases just enough of the
documented INF directive semantics to enumerate the package's payload files -
the files a real installation would copy onto the target machine:

- the `[Manufacturer]` section, indirecting to one or more `Models` sections
  ([INF Manufacturer Section](https://learn.microsoft.com/windows-hardware/drivers/install/inf-manufacturer-section))
- each `Models` section's `device-description=install-section-name,hwid[,...]`
  entries
  ([INF Models Section](https://learn.microsoft.com/windows-hardware/drivers/install/inf-models-section))
- the `install-section-name.CopyFiles` directive, whose value is either
  `@filename` (a direct copy using `DefaultDestDir`) or a list of
  file-list-section names
  ([INF CopyFiles Directive](https://learn.microsoft.com/windows-hardware/drivers/install/inf-copyfiles-directive))
- `[SourceDisksFiles]` / `[SourceDisksFiles.<arch>]`, mapping a source file
  name to a disk ID and optional subdirectory
  ([INF SourceDisksFiles Section](https://learn.microsoft.com/windows-hardware/drivers/install/inf-sourcedisksfiles-section))
- `[DestinationDirs]` and the standard numeric DIRID directory-ID values
  (`DirID*` constants in `dirid.go`)
  ([INF DestinationDirs Section](https://learn.microsoft.com/windows-hardware/drivers/install/inf-destinationdirs-section),
  [Using Dirids](https://learn.microsoft.com/windows-hardware/drivers/install/using-dirids))
- the `[Version]` section's `CatalogFile`/`CatalogFile.<platform>` entry
  (via `inf.File.CatalogFileForPlatform`), naming the accompanying `.cat` file
- the `AddService` directive chain: an install section's (platform-decorated)
  `<install-section-name>.Services` section
  ([INF DDInstall.Services Section](https://learn.microsoft.com/windows-hardware/drivers/install/inf-ddinstall-services-section))
  containing one or more `AddService` directives
  ([INF AddService Directive](https://learn.microsoft.com/windows-hardware/drivers/install/inf-addservice-directive)),
  each naming a service-install-section with `ServiceType`, `StartType`,
  `ErrorControl`, `ServiceBinary` (`%dirid%\path`, resolved via the same
  `DirID` model as `dirid.go`/`PayloadFile`), and optional `LoadOrderGroup`
  and `Dependencies` (`service.go`'s `ServiceInstall`, `(*Package).Services`)
- the documented `HKLM\SYSTEM\CurrentControlSet\Services\<name>` registry
  schema (`Type`/`Start`/`ErrorControl`/`ImagePath`/`Group`/`DependOnGroup`/
  `DependOnService`), and the `Select\Default` -> `ControlSetNNN` resolution a
  SYSTEM hive's "current control set" conventionally requires - both of these
  are generic, INF-independent Windows-service-registry concepts, and now
  live in the sibling [`service`](../service) package
  (`service.Install`/`service.CurrentControlSet`) rather than in `driver`
  itself. `registryinstall.go`'s `InstallRegistry` resolves each
  `ServiceInstall`'s `BinaryDirID`+`BinaryPath` into a plain `ImagePath`
  string and delegates the actual registry merge to `service.Install`, so
  `driver`'s own remaining scope here is just the INF-specific part: parsing
  the `AddService` directive chain and resolving its `%dirid%\path` token. A
  caller that already has a fully-resolved service registration in hand (not
  derived from an INF) can depend on `service` alone, without pulling in
  `driver`'s much heavier `inf`/`cat`/`pe`/`wim` dependency set.
- the `CriticalDeviceDatabase` mechanism for registering a device's hardware
  ID against a service before PnP ever sees the device (see
  `criticaldevicedatabase.go`'s citations), which - unlike Services - *is*
  PnP/driver-install-specific, so it stays in `driver` and is merged into a
  caller-supplied `*regf.Key` tree via the sibling [`regf`](../regf) package,
  reusing `service`'s exported `FindOrCreateSubkey`/`SetValue` navigation
  helpers (`registryinstall.go`'s `mergeCriticalDeviceDatabase`)

It deliberately simplifies platform/OS-version selection: rather than
evaluating `TargetOSVersion` decorations (OS major/minor version, product
type, suite mask, build number), it unions the entries of every `<name>`,
`<name>.NT`, and `<name>.NT<platform>` section variant for the single
caller-supplied architecture token, and does not resolve the multi-disk
`[SourceDisksNames]` "disk root"/tag-file mechanism - a payload file's
location is always the optional `SourceDisksFiles` subdir underneath the
INF's own directory (a single, already-unpacked driver source tree).

It **deliberately does not** implement:

- The Windows DriverStore's FileRepository path-hashing scheme (the
  `<infname>_<hash>` folder naming under
  `\Windows\System32\DriverStore\FileRepository\`). That scheme is
  undocumented/reverse-engineered, not sourced from an authoritative spec,
  and this repo's policy is to never speculate about undocumented internals.
  This was checked empirically rather than just assumed: extracting the real
  `FileRepository`/`INF` directories from a Windows 11 23H2 `install.esd`
  showed byte-identical copies of e.g. `1394.inf` stored under
  `1394.inf_amd64_f05cd2933ff9e649`, but MD5, SHA-1, and SHA-256 of that exact
  file (full digest and both truncated ends) all disagree with the folder's
  16 hex-character suffix, across every package checked - so the suffix is
  not a simple hash of the INF's bytes, and reproducing it would mean
  reverse-engineering (or replicating unknown internal state of)
  `setupapi.dll`/`drvstore.dll`, which is out of scope here. `Install`
  instead takes the destination directory path for each DIRID used by the
  package as an explicit parameter.
- The `SYSTEM` hive's `DriverDatabase` key tree
  (`SYSTEM\DriverDatabase\DriverPackages`, `DeviceIds`, etc), the internal
  driver-ranking database PnP uses to choose among multiple matching drivers
  for a device. Checked, not just assumed: a documentation search turned up
  no authoritative Microsoft Learn (or equivalent) page describing this
  schema - unlike `CriticalDeviceDatabase` and the `Services` key schema,
  both of which are documented (see `registryinstall.go`'s and the sibling
  [`service`](../service) package's citation trails).
- The `Enum` device-instance tree
  (`SYSTEM\CurrentControlSet\Enum\<enumerator>\<device-id>\<instance-id>`).
  Registering an actual device instance requires a real, live hardware
  instance ID discovered by PnP enumeration at boot/setup time, which an
  offline image-prep tool does not have.
- `INFCACHE.1` (the binary cache of parsed INF metadata SetupAPI maintains
  under `%SystemRoot%\INF`): a distinct, undocumented binary format in its
  own right.
- `AddReg`/`DelReg` directives in general, beyond the specific `Services` and
  `CriticalDeviceDatabase` registry values enumerated above: a generic, much
  broader directive mechanism this package does not take on.
- PnP class-installer semantics and driver ranking/selection among multiple
  matching drivers for the same device (exactly what the undocumented
  `DriverDatabase` non-goal above exists to select among).
- Registry-hive writing back to disk (deciding which hive file to open,
  backing it up, replacing it) - this package only produces/merges
  `regf.Key`/`regf.Value` structures given an already-loaded `*regf.Hive`/
  `*regf.Key`; the caller handles file I/O.
- Authenticode signature verification or X.509 certificate validation - relies
  entirely on `cat`'s own non-goals; `Verify` performs only structural hash
  comparison.
- The final WIM-file writer (resource offset assignment, header/blob-table
  serialization into a new output file) - that belongs in a future addition to
  `wim`, not here. `Install` returns in-memory `*wim.DirEntry` nodes and a
  slice of new blob content, given an existing `*wim.ImageMetadata` /
  `*wim.BlobTable` to extend.

## Layout

| File | Responsibility |
|------|----------------|
| `driver.go` | package doc (citations for the modeled directive semantics and non-goals), `wrapErr` |
| `dirid.go` | `DirID`, the standard `DirID*` DIRID constants |
| `load.go` | `Package`, `PayloadFile`, `LoadPackage` (INF parse + catalog resolution + CopyFiles/SourceDisksFiles/DestinationDirs enumeration) |
| `verify.go` | `VerifyStatus`, `FileVerification`, `(*Package).Verify` |
| `install.go` | `NewBlob`, `Install` (merge payload files into a `*wim.ImageMetadata` + `*wim.BlobTable`) |
| `service.go` | `ServiceInstall`, `(*Package).Services` (AddService directive chain resolution) |
| `criticaldevicedatabase.go` | `CriticalDeviceDatabaseEntry`, `(*Package).CriticalDeviceDatabaseEntries`, `CriticalDeviceDatabaseSubkeyName` |
| `registryinstall.go` | `InstallRegistry` (resolve each `ServiceInstall`'s `ImagePath` and delegate to `service.Install`; merge CriticalDeviceDatabase into a `*regf.Key` tree via `service`'s exported navigation helpers) |
| `driver_test.go` | synthetic INF/catalog/PE fixtures and tests (payload files, verify, WIM install) |
| `driver_registry_test.go` | synthetic INF/registry fixtures and tests (services, CDDB, control-set resolution, registry install) |

## Usage

```go
// import "github.com/Pandapip1/gowim/service" for CurrentControlSet below.

fsys := os.DirFS("/path/to/extracted/driver/package")

pkg, err := driver.LoadPackage(fsys, "contoso.inf", "amd64")
if err != nil {
    log.Fatal(err)
}

// Structural hash check against the package's catalog.
results, err := pkg.Verify()
if err != nil {
    log.Fatal(err)
}
for _, r := range results {
    fmt.Printf("%s: %s\n", r.File.DestName, r.Status)
}

// Merge the package's payload files into an existing image's metadata and
// blob table. The caller supplies the destination path for every DIRID the
// package's files use (this package does not compute DriverStore paths).
destDirs := map[driver.DirID]string{
    driver.DirIDDriverStore: `Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_<...>`,
}
root, newBlobs, err := driver.Install(imageMetadata, blobTable, pkg, destDirs)
if err != nil {
    log.Fatal(err)
}
// Place newBlobs' raw bytes in the eventual output WIM file (not implemented
// by this package - see wim's stated scope), then serialize root/imageMetadata
// and blobTable as usual.

// Merge the package's Services and CriticalDeviceDatabase registry
// registration into a SYSTEM hive's current control set. destDirs is the
// same map used above for Install. CurrentControlSet now lives in the
// sibling service package (a deliberate, documented API move - see that
// package's README) - InstallRegistry's own signature/behavior is
// unchanged.
currentControlSet, err := service.CurrentControlSet(systemHive.Root)
if err != nil {
    log.Fatal(err)
}
if err := driver.InstallRegistry(currentControlSet, pkg, destDirs, "amd64"); err != nil {
    log.Fatal(err)
}
// Serialize systemHive (regf.Hive.AppendTo) as usual; this package does not
// write the hive file back to disk itself.
```

## Tests

```
go test ./...
```

`driver_test.go` hand-builds: a minimal but structurally valid PE32+ `.sys`
payload (the same construction approach as `pe/pe_test.go`'s fixtures), a
synthetic (unsigned) catalog wrapping a `CertificateTrustList` with `File` and
digest attributes for the payload files (the same construction approach as
`cat/cat_test.go`), and a driver `.inf` exercising `[Manufacturer]` ->
`Models` -> `install-section.CopyFiles` -> `[SourceDisksFiles]` /
`[DestinationDirs]` (`DefaultDestDir` = DIRID 13). Tests assert: `LoadPackage`
enumerates the expected payload files with the expected source path and
DIRID; `Verify` reports all-OK against the synthetic catalog and reports a
mismatch when a payload file's bytes are corrupted after the catalog was
built; `Install` produces `DirEntry` nodes at the expected path with the
right stream hash, rejects a corrupt `.sys` payload, requires a destination
directory for every DIRID used, and dedupes blob-table entries by hash
(bumping `RefCount`) rather than duplicating them when installing the same
package twice.

`driver_registry_test.go` extends the synthetic INF with a hardware ID +
compatible ID on the `Models` entry and an `.Services`/`AddService`/
service-install-section chain, and hand-builds a minimal SYSTEM-hive-shaped
`*regf.Key` tree (`Select`, `ControlSet001`, `ControlSet001\Services`,
`ControlSet001\Control\CriticalDeviceDatabase`), using the same struct-literal
construction style as `regf/regf_test.go`. Tests assert: `Services` enumerates
the expected `ServiceInstall` (including the `AssocService` bit and parsed
`Dependencies`/`LoadOrderGroup`); `CriticalDeviceDatabaseEntries` enumerates
one entry per hardware/compatible ID with the right `ClassGuid`/`Service`;
`InstallRegistry` produces the expected `Services\<name>` and
`CriticalDeviceDatabase\<hwid>` subkeys/values, is idempotent (a second call
does not duplicate subkeys or values), and the merged tree round-trips
through `regf.Hive.AppendTo`/`regf.Parse`. `CurrentControlSet`'s own
resolution logic is now tested by the sibling
[`service`](../service) package; `driver_registry_test.go` still calls
`service.CurrentControlSet` to build a realistic tree for `InstallRegistry`'s
tests.

## License

MIT OR Apache-2.0.
