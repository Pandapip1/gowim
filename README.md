# gowim

A Go reimplementation of the on-disk handling of Windows imaging and driver
formats. This is a Go workspace (`go.work`) containing several independent
modules, each covering one file format's structure, parsing, and
serialization. Every module is modeled directly on the relevant authoritative
specification (Microsoft documentation, RFCs, or reference implementations
such as [wimlib](https://wimlib.net)), and deliberately limits itself to
container *structure* rather than full semantic interpretation — see each
module's own README for its precise scope and non-goals.

## Modules

| Module | Format | Status |
|--------|--------|--------|
| [`wim/`](wim/README.md) | WIM (Windows Imaging Format) container | done |
| [`inf/`](inf/README.md) | INF (driver installation information) files | done |
| [`cat/`](cat/README.md) | CAT (Windows Catalog / PKCS#7 signed catalog) files | done |
| [`pe/`](pe/README.md) | PE/COFF container (used for `.sys` driver binaries) | done |
| [`driver/`](driver/README.md) | ties `inf`+`cat`+`pe`+`wim` together: load a driver package, verify its files against its catalog, and build the WIM-side tree/blob additions to install it | done |

These support installing `.inf`/`.cat`/`.sys` driver packages into WIM images
— `inf`, `cat`, and `pe` handle the three file formats that make up a driver
package, `wim` handles the container they get installed into, and `driver`
ties them together. `driver` deliberately does not compute Windows'
DriverStore FileRepository path-hashing scheme or edit registry hives (see
its README) — callers supply destination paths, and a final WIM-file writer
(assembling a complete output file with real resource offsets) is still a
future addition to `wim`.

## Working in this repo

This is a multi-module workspace. From the repo root:

```
go build ./wim/... ./inf/... ./cat/... ./pe/... ./driver/...
go test  ./wim/... ./inf/... ./cat/... ./pe/... ./driver/...
```

(Plain `./...` doesn't resolve from the workspace root since it isn't itself
a module; either `cd` into a module directory or name modules explicitly as
above.)

## License

MIT OR Apache-2.0.
