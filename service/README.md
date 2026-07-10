# gowim/service

A small Go package that models the Windows Service Control Manager's (SCM)
registry schema — the `HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services`
key tree that `CreateService`/`ChangeServiceConfig`/`ChangeServiceConfig2`
document themselves as writing to — and reads, installs, modifies, deletes,
and enables/disables a service registration in a caller-supplied `*regf.Key`
tree.

It depends on nothing but the Go standard library and the sibling
[`regf`](../regf) package, so "manage a service registration in a registry
hive" is usable entirely on its own, without pulling in the much heavier
dependency set (`inf`, `cat`, `pe`, `wim`) the sibling [`driver`](../driver)
package needs purely to parse driver packages — not to define what a Windows
service registration looks like in the registry.

Service-schema semantics (`Type`/`Start`/`ErrorControl`/`ImagePath`/`Group`/
`DisplayName`/`Description`/`ObjectName`/`DependOnGroup`/`DependOnService`)
are cross-checked against the official Win32
[`CreateServiceW` function (winsvc.h)](https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew),
[`ChangeServiceConfigW` function (winsvc.h)](https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-changeserviceconfigw),
and
[`ChangeServiceConfig2W` function (winsvc.h)](https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-changeserviceconfig2w)
documentation, the
[`SERVICE_DESCRIPTIONW` structure (winsvc.h)](https://learn.microsoft.com/windows/win32/api/winsvc/ns-winsvc-service_descriptionw)
documentation, and the documented
[`HKLM\SYSTEM\CurrentControlSet\Services` registry tree](https://learn.microsoft.com/windows-hardware/drivers/install/hklm-system-currentcontrolset-services-registry-tree),
all on Microsoft Learn, as of 2026-07-10; see the citations in `service.go`
and `install.go`.

## Scope

- `Service`, a fully-resolved Windows service registration: `Name`, `Type`,
  `Start`, `ErrorControl`, `ImagePath` (a plain, already-resolved path
  string), `Group`, `DisplayName`, `Description`, `ObjectName` (the "Log On
  As" account *name* only — see the non-goals below for the account
  *password*), `DependOnGroup`, `DependOnService`.
- Named `Type*`/`Start*`/`ErrorControl*` constants for the values
  `CreateService`'s `dwServiceType`/`dwStartType`/`dwErrorControl` parameters
  document.
- `Install`, which creates/updates a `Services\<Name>` subkey under a
  caller-supplied `*regf.Key` with the documented value names/types,
  finding-or-creating the subkey (so it never errors if the service does not
  yet exist).
- `Modify`, which updates a `Services\<Name>` subkey using the exact same
  value-writing logic as `Install`, but requires the subkey to already exist
  — it returns an error wrapping `ErrNotFound` (checkable with `errors.Is`)
  instead of creating one, so a typo'd/missing service name fails loudly
  rather than silently installing a stray registration.
- `Read`, the reverse of `Install`/`Modify`: parses an existing
  `Services\<Name>` subkey back into a `Service`. Returns an error wrapping
  `ErrNotFound` if the subkey does not exist.
- `Delete`, which removes a `Services\<Name>` subkey (and everything under
  it) entirely. Returns an error wrapping `ErrNotFound` if the subkey does
  not exist — deleting a service that was never there, or deleting it
  twice, is treated as a caller error, not a no-op.
- `SetStartType`, `Enable`, and `Disable` — convenience wrappers around
  `Read`+`Modify` that change just a service's `Start` value without
  requiring the caller to reconstruct the whole `Service`. `Enable` rejects
  `StartDisabled` (that's what `Disable` is for) with a clear error. All
  three return an error wrapping `ErrNotFound` for a nonexistent service.
- `List`, which returns the name of every service currently registered
  directly under a `Services` key (in subkey order). It only enumerates
  names — it does not `Read` each one, so it cannot fail on an individual
  malformed entry; call `Read` per name for full details, skipping any that
  error.
- `ErrNotFound`, the sentinel error `Read`/`Modify`/`Delete`/`SetStartType`/
  `Enable`/`Disable` all wrap (via `%w`, so `errors.Is(err,
  service.ErrNotFound)` works) when the named service has no existing
  `Services\<Name>` subkey to operate on.
- `CurrentControlSet`, which resolves a SYSTEM hive's `Select\Default` ->
  `ControlSetNNN` indirection — generic SYSTEM-hive knowledge, included here
  (rather than split into yet another module) so this package is usable
  entirely on its own to find where the `Services` key belongs.
- Exported `*regf.Key`/`*regf.Value` navigation helpers (`FindSubkey`,
  `FindOrCreateSubkey`, `RemoveSubkey`, `FindValue`, `SetValue`,
  `RemoveValue`) that `Install`/`Modify`/`Delete` are themselves built from,
  and that the sibling `driver` package reuses for its own
  `CriticalDeviceDatabase` merging rather than keeping a second, private copy
  of the same find-or-create logic.

It deliberately does **not** implement:

- Any live SCM API semantics: starting, stopping, querying, or otherwise
  controlling a running service. This applies to every function in this
  package, not just `Install` — `Read`/`Modify`/`Delete`/`SetStartType`/
  `Enable`/`Disable` also only read/write the on-disk registry shape those
  APIs are documented to persist; none of them ever talks to a running
  service control manager.
- The `DriverDatabase` key tree, the `Enum` device-instance tree, or
  `INFCACHE.1` — see the sibling [`driver`](../driver) package's README for
  why `driver` does not implement these either; they are not a "service"
  concept at all, so this package does not even discuss them at length.
- `CriticalDeviceDatabase` — registering a hardware ID against a service
  before PnP ever sees the device is a PnP/driver-install concept, not a
  generic "service" concept, and stays entirely in the sibling `driver`
  package.
- Registry-hive file I/O (deciding which hive file to open, backing it up,
  replacing it): this package only produces/merges/reads `regf.Key`/
  `regf.Value` structures given an already-loaded (or freshly-built)
  `*regf.Key` tree; the caller handles file I/O.
- Resolving *where a service's binary comes from* — an INF's
  `%dirid%\path` token, a plain absolute path, or anything else. That
  resolution is entirely the caller's job; this package only writes an
  already-resolved `Service.ImagePath` string.
- Recovery/failure-action configuration: `ChangeServiceConfig2`'s
  `SERVICE_CONFIG_FAILURE_ACTIONS` info level and the registry's
  `FailureActions` (`REG_BINARY`) value, plus the related
  `FailureActionsOnNonCrashFailures`/`RebootMessage` values. This was
  actually checked, not assumed: the
  [`SERVICE_FAILURE_ACTIONSW` structure](https://learn.microsoft.com/windows/win32/api/winsvc/ns-winsvc-service_failure_actionsw)
  docs document the in-memory C struct `ChangeServiceConfig2`/
  `QueryServiceConfig2` marshal to/from, but no authoritative Microsoft
  source documents the actual on-disk byte layout of the `FailureActions`
  `REG_BINARY` registry value itself; third-party notes such as the
  [winreg-kb project's "Services and drivers" page](https://winreg-kb.readthedocs.io/en/latest/sources/system-keys/Services-and-drivers.html)
  list `FailureActions` among the value names present under a service key
  but document no format for it, confirming the gap rather than filling it.
  This package therefore does not implement encoding/decoding
  `FailureActions`, mirroring the sibling `driver` package's
  DriverStore-hash and `DriverDatabase` non-goals: an undocumented on-disk
  format is not something this package will guess at.
- The service account's *password*: LSA secrets in the SECURITY hive — a
  separate, far more sensitive encrypted-blob mechanism, not a plain
  `Services\<Name>` registry value — are entirely out of scope.
  `Service.ObjectName` is just the account *name* string.

## Layout

| File | Responsibility |
|------|----------------|
| `service.go` | package doc, `Service`, `Type*`/`Start*`/`Error*` constants, `ErrNotFound`, `wrapErr` |
| `encoding.go` | `stringToUTF16LE`/`utf16LEToString`/`multiSZToStrings` |
| `keys.go` | `FindSubkey`/`FindOrCreateSubkey`/`RemoveSubkey`/`FindValue`/`SetValue`/`RemoveValue`, `uint32LEBytes`/`multiSZBytes` encoders, `setOrRemoveSZ`, `readDWORD`/`readSZ`/`readMultiSZ` decoders |
| `install.go` | `Install`, plus `validateService`/`writeServiceValues` (the merge logic shared with `Modify`) |
| `modify.go` | `Modify` (like `Install`, but requires the service to already exist) |
| `read.go` | `Read` (parse an existing `Services\<Name>` subkey back into a `Service`) |
| `delete.go` | `Delete` (remove a `Services\<Name>` subkey entirely) |
| `enable.go` | `SetStartType`, `Enable`, `Disable` |
| `list.go` | `List` (enumerate service names under a `Services` key) |
| `controlset.go` | `CurrentControlSet` (`Select\Default` -> `ControlSetNNN` resolution) |
| `service_test.go`, `read_modify_delete_test.go`, `list_test.go` | fixtures and tests |

## Usage

```go
root := systemHive.Root // an already-loaded or freshly-built *regf.Key

currentControlSet, err := service.CurrentControlSet(root)
if err != nil {
    log.Fatal(err)
}
servicesKey := service.FindOrCreateSubkey(currentControlSet, "Services")

err = service.Install(servicesKey, service.Service{
    Name:            "ContosoDrv",
    Type:            service.TypeKernelDriver,
    Start:           service.StartDemand,
    ErrorControl:    service.ErrorNormal,
    ImagePath:       `\Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_deadbeef\driver.sys`,
    Group:           "Extended Base",
    DisplayName:     "Contoso Driver",
    Description:     "Does contoso things.",
    ObjectName:      "LocalSystem",
    DependOnGroup:   []string{"NetBIOSGroup"},
    DependOnService: []string{"RpcSs"},
})
if err != nil {
    log.Fatal(err)
}

// Later, e.g. after re-opening/re-parsing the hive:
svc, err := service.Read(servicesKey, "ContosoDrv")
if err != nil {
    log.Fatal(err) // errors.Is(err, service.ErrNotFound) if it's gone
}
svc.Start = service.StartDemand
if err := service.Modify(servicesKey, svc); err != nil {
    log.Fatal(err)
}

// Or, without needing to Read the whole Service first:
if err := service.Disable(servicesKey, "ContosoDrv"); err != nil {
    log.Fatal(err)
}
if err := service.Enable(servicesKey, "ContosoDrv", service.StartDemand); err != nil {
    log.Fatal(err)
}

if err := service.Delete(servicesKey, "ContosoDrv"); err != nil {
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

`read_modify_delete_test.go` covers the newer read/write/delete surface:
`Read` round-trips everything `Install` writes, including
`DisplayName`/`Description`/`ObjectName` and both dependency lists; `Read`
on a nonexistent service returns `ErrNotFound`; `Modify` on a nonexistent
service returns `ErrNotFound` and does not create one; `Modify` on an
existing service updates it and clears fields it no longer sets; `Delete`
removes an existing service and returns `ErrNotFound` on a second `Delete`
or an always-missing name; and `SetStartType`/`Enable`/`Disable` change only
`Start`, leaving every other field untouched, with `Enable(...,
StartDisabled)` returning an error.

## License

MIT OR Apache-2.0.
