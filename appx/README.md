# gowim/appx

Implements enough of the AppX/MSIX package-identity model, and Windows'
offline (un-booted, pre-OOBE) provisioned-package bookkeeping, to remove a
provisioned package from a factory Windows image without booting it or
shelling out to real DISM.

## Scope

- `ParseManifest` decodes an `AppxManifest.xml`'s `<Identity>` element
  (`Name`/`Publisher`/`Version`/`ProcessorArchitecture`/`ResourceId`) --
  officially documented by Microsoft's "Package manifest schema reference
  for Windows 10". Only `Identity` is modeled: enough to identify a
  package, not to reproduce app-activation/manifest semantics.
- `PublisherID`/`PackageFamilyName` compute a package's 13-character
  Crockford Base32 publisher ID and `<name>_<publisherId>` family name from
  its `Identity` (SHA-256 of the UTF-16LE-encoded `Publisher` string,
  Crockford Base32-encode the first 8 bytes). Not published verbatim by
  Microsoft as prose (only exposed via the `PackageFamilyNameFromId` Win32
  API); implemented by directly reading, not guessing from prose, the
  reference Rust reimplementation
  [russellbanks/package-family-name](https://github.com/russellbanks/package-family-name)
  -- see `familyname.go`'s doc comments.
- `ParseProvisioning`/`ProvisionList.Serialize` decode/encode
  `AppxProvisioning.xml`, the offline (pre-boot) source of truth for which
  packages a Windows image provisions for all users (`<Provisioned>`) and
  which package families are blocked from being (re)provisioned
  (`<EndOfLife>`). Not documented by Microsoft under this filename;
  reverse-engineered by direct inspection of a real Windows 11 23H2 image.
- `RemoveProvisioned`/`Remove` perform offline provisioned-package removal:
  removing a package family's `<Provisioned>` entries (plus its
  bundle/resource/dependency siblings) and optionally adding it to
  `<EndOfLife>`; deleting its `Applications\<PackageFullName>` SOFTWARE-hive
  subkey (via the sibling [`regf`](../regf)/[`registry`](../registry)
  packages); adding an `AppxAllUserStore\Deprovisioned\<PackageFamilyName>`
  marker subkey; and deleting its
  `Program Files\WindowsApps\<PackageFullName>` directory from the image's
  tree (via the sibling [`wim`](../wim) package's `DirEntry.Remove`),
  decrementing blob-table refcounts first, mirroring the sibling
  [`driver`](../driver) package's `Uninstall`.

## Explicit non-goals

- Anything requiring the runtime `StateRepository-Machine.srd` SQLite
  database: confirmed absent on a pristine, un-booted image (only created
  at first boot/specialize), so out of scope for offline servicing
  entirely.
- Full `AppxManifest.xml` parsing beyond `<Identity>`.
- Authenticode/package signature verification.

## Usage

```go
pl, err := appx.ParseProvisioning(provisioningXMLBytes)

hs, err := registry.LoadHiveSet(r, meta.Root, bt)
software := hs.Hives[registry.HiveSoftware]
applications := software.Hive.Root.FindOrCreatePath(appx.ApplicationsPath)
deprovisioned := software.Hive.Root.FindOrCreatePath(appx.DeprovisionedPath)

err = appx.Remove(pl, "Microsoft.Paint_8wekyb3d8bbwe", true, applications, deprovisioned, meta.Root, bt)

newBlob, err := software.Save(bt)
provisioningData, err := pl.Serialize() // write back to AppxProvisioning.xml's path in the image
```

## Tests

```
go test ./...
```

Tests are built against real Windows 11 23H2 fixtures copied verbatim
(2026-07-14, via a read-only `guestmount` of the project's win11 VM disk)
into `testdata/`: a real `AppxManifest.xml` and a real
`AppxProvisioning.xml`, cross-checked against the real image's SOFTWARE
hive registry tree (`hivexregedit --export`). `PublisherID` is additionally
cross-checked against `github.com/russellbanks/package-family-name`'s own
Rust test vectors.

## License

MIT OR Apache-2.0.
