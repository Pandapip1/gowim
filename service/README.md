# gowim/service

A small Go package that models the Windows Service Control Manager's (SCM)
registry schema — the `HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services`
key tree that `CreateService` documents itself as writing to — and merges an
already-resolved service registration into a caller-supplied `*regf.Key`
tree.

It depends on nothing but the Go standard library and the sibling
[`regf`](../regf) package, so "add a service to a registry hive" is usable
entirely on its own, without pulling in the much heavier dependency set
(`inf`, `cat`, `pe`, `wim`) the sibling [`driver`](../driver) package needs
purely to parse driver packages — not to define what a Windows service
registration looks like in the registry.

Service-schema semantics (`Type`/`Start`/`ErrorControl`/`ImagePath`/`Group`/
`DependOnGroup`/`DependOnService`) are cross-checked against the official
Win32
[`CreateServiceW` function (winsvc.h)](https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew)
documentation on Microsoft Learn, and the documented
[`HKLM\SYSTEM\CurrentControlSet\Services` registry tree](https://learn.microsoft.com/windows-hardware/drivers/install/hklm-system-currentcontrolset-services-registry-tree),
as of 2026-07-10; see the citations in `service.go` and `install.go`.

## Scope

- `Service`, a fully-resolved Windows service registration: `Name`, `Type`,
  `Start`, `ErrorControl`, `ImagePath` (a plain, already-resolved path
  string), `Group`, `DependOnGroup`, `DependOnService`.
- Named `Type*`/`Start*`/`ErrorControl*` constants for the values
  `CreateService`'s `dwServiceType`/`dwStartType`/`dwErrorControl` parameters
  document.
- `Install`, which creates/updates a `Services\<Name>` subkey under a
  caller-supplied `*regf.Key` with the documented value names/types.
- `CurrentControlSet`, which resolves a SYSTEM hive's `Select\Default` ->
  `ControlSetNNN` indirection — generic SYSTEM-hive knowledge, included here
  (rather than split into yet another module) so this package is usable
  entirely on its own to find where the `Services` key belongs.
- Exported `*regf.Key`/`*regf.Value` navigation helpers (`FindSubkey`,
  `FindOrCreateSubkey`, `FindValue`, `SetValue`, `RemoveValue`) that `Install`
  itself is built from, and that the sibling `driver` package reuses for its
  own `CriticalDeviceDatabase` merging rather than keeping a second, private
  copy of the same find-or-create logic.

It deliberately does **not** implement:

- Any live SCM API semantics: starting, stopping, querying, or otherwise
  controlling a running service. This package only reads/writes the on-disk
  registry shape those APIs are documented to persist; it never talks to a
  running service control manager.
- The `DriverDatabase` key tree, the `Enum` device-instance tree, or
  `INFCACHE.1` — see the sibling [`driver`](../driver) package's README for
  why `driver` does not implement these either; they are not a "service"
  concept at all, so this package does not even discuss them at length.
- `CriticalDeviceDatabase` — registering a hardware ID against a service
  before PnP ever sees the device is a PnP/driver-install concept, not a
  generic "service" concept, and stays entirely in the sibling `driver`
  package.
- Registry-hive file I/O (deciding which hive file to open, backing it up,
  replacing it): `Install` only produces/merges `regf.Key`/`regf.Value`
  structures given an already-loaded (or freshly-built) `*regf.Key` tree; the
  caller handles file I/O.
- Resolving *where a service's binary comes from* — an INF's
  `%dirid%\path` token, a plain absolute path, or anything else. That
  resolution is entirely the caller's job; this package only writes an
  already-resolved `Service.ImagePath` string.

## Layout

| File | Responsibility |
|------|----------------|
| `service.go` | package doc, `Service`, `Type*`/`Start*`/`Error*` constants, `wrapErr` |
| `encoding.go` | `stringToUTF16LE` |
| `keys.go` | `FindSubkey`/`FindOrCreateSubkey`/`FindValue`/`SetValue`/`RemoveValue`, `uint32LEBytes`/`multiSZBytes` |
| `install.go` | `Install` (merge a `Service` into a `Services\<Name>` subkey) |
| `controlset.go` | `CurrentControlSet` (`Select\Default` -> `ControlSetNNN` resolution) |
| `service_test.go` | fixtures and tests |

## Usage

```go
root := systemHive.Root // an already-loaded or freshly-built *regf.Key

currentControlSet, err := service.CurrentControlSet(root)
if err != nil {
    log.Fatal(err)
}
servicesKey := service.FindOrCreateSubkey(currentControlSet, "Services")

err = service.Install(servicesKey, service.Service{
    Name:         "ContosoDrv",
    Type:         service.TypeKernelDriver,
    Start:        service.StartDemand,
    ErrorControl: service.ErrorNormal,
    ImagePath:    `\Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_deadbeef\driver.sys`,
    Group:        "Extended Base",
    DependOnGroup:   []string{"NetBIOSGroup"},
    DependOnService: []string{"RpcSs"},
})
if err != nil {
    log.Fatal(err)
}
// Serialize systemHive (regf.Hive.AppendTo) as usual; this package does not
// write the hive file back to disk itself.
```

## Tests

```
go test ./...
```

`service_test.go` hand-builds a minimal SYSTEM-hive-shaped `*regf.Key` tree
(`Select`, `ControlSet001`, `ControlSet001\Services`), using the same
struct-literal construction style as `regf/regf_test.go`. Tests assert:
`CurrentControlSet` resolves the right `ControlSetNNN` via `Select\Default`;
`Install` produces the expected `Services\<name>` subkey/values, is
idempotent (a second call does not duplicate subkeys or values, and clears
`Group`/`DependOnGroup`/`DependOnService` once the incoming `Service` no
longer sets them); and the merged tree round-trips through
`regf.Hive.AppendTo`/`regf.Parse`.

## License

MIT OR Apache-2.0.
