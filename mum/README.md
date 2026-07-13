# gowim/mum

A Go implementation of Windows servicing package manifests (`.mum` files,
e.g. `Windows\servicing\Packages\*.mum`): the plain-XML documents CBS
(Component-Based Servicing) uses to declare a package's identity, its
relationship to other packages (parent/update/dependency), and
optional-feature selectability.

The base `<assembly>`/`assemblyIdentity` schema (`asm.v1`/`asm.v3`
namespaces) is documented by Microsoft: [Assembly manifests -- Microsoft
Learn](https://learn.microsoft.com/en-us/windows/win32/sbscs/assembly-manifests),
[Manifest file schema --
Microsoft Learn](https://learn.microsoft.com/en-us/windows/win32/sbscs/manifest-file-schema).
The CBS-specific `asm.v3` vocabulary modeled here (`<package>`, `<update>`,
`<parent>`, `<installerAssembly>`, `<selectable>`, `<detectNone>`,
`<declareCapability>`, `<component>`) is **not** documented anywhere found
-- it was empirically inferred, per this repo's standing reverse-engineering
policy, from two rounds of real-data inspection recorded in the top-level
`TODO.md`: 1262 real `.mum` files sampled from a Windows 11 23H2 image
(2026-07-10), and a further cross-check against a real, running Windows VM
(`guestmount --ro` against `/var/lib/libvirt/images/win11.qcow2`,
2026-07-13) used to derive and verify this package's test fixtures.

## Scope

This package handles `.mum` files only -- plain UTF-8 XML. It does **not**
handle WinSxS `.manifest` files (`Windows\WinSxS\Manifests\*.manifest`):
those are PA30-delta-compressed, a separate, still under-research binary
format (see `TODO.md`'s CBS/servicing section), not XML.

Within `.mum` files, this package models exactly the element/attribute
vocabulary confirmed present across the real samples above:

- root `<assembly>` attributes (`manifestVersion`, `copyright`,
  `description`, `displayName`, `company`, `supportInformation`,
  `creationTimeStamp`, `lastUpdateTimeStamp`) and its `<assemblyIdentity>`
- `<package>`: `identifier`/`releaseType`/`restart`, nested `<parent>`,
  `<installerAssembly>`, `<declareCapability>`, and any number of `<update>`
  children
- `<update>`: `name`/`displayName`/`description`, and a nested
  `<selectable>` (+ `<detectNone>`), `<package>`, or `<component>`
- `<declareCapability>`: a provided `<capability><capabilityIdentity>` and
  any number of required `<dependency><capabilityIdentity>` entries

It **deliberately does not** model every element real `.mum` files can
contain -- the format has substantially more vocabulary
(`<driver>`, `<satelliteInfo>`, `<MutualExclusionGroup>`,
`<NonAncestorDependencies>`, vendor extensions like
`<mum2:customInformation>`, etc., per a raw-element survey across all 2532
real `.mum` files in the `win11.qcow2` sample) than is needed to identify a
package's identity, its declared payload/component references, and its
dependency edges -- the scope needed for the component-store work this
package feeds into. `Parse` silently ignores unmodeled elements
(`encoding/xml`'s default behavior); `Serialize` therefore does not
losslessly round-trip a manifest containing them. See
`TestSerializeDropsUnmodeledExtensions` in `mum_test.go`, which documents
this limitation as a passing test rather than leaving it implicit.

## Usage

```go
data, _ := os.ReadFile("KB5030219.mum")

m, err := mum.Parse(data)
if err != nil {
    log.Fatal(err)
}
fmt.Println(m.Identity.Name, m.Package.Identifier)
for _, u := range m.Package.Updates {
    if u.Package != nil {
        fmt.Println(" ->", u.Package.Identity.Name)
    }
}

out, err := m.Serialize() // re-encode to XML
```

## Tests

```
go test ./...
```

`testdata/*.mum` are real files copied verbatim (not hand-constructed) from
a real Windows 11 23H2 VM's `Windows\servicing\Packages` directory
(2026-07-13), chosen to cover the distinct shapes this package models: a
plain package/update chain, a KB wrapper (`<parent>` +
`<installerAssembly>`), an optional-feature manifest (`<selectable>` +
`<detectNone>`), and a language-feature manifest (`<declareCapability>` +
`<dependency>` + nested `<component>`, which also contains the
`<mum2:customInformation>` vendor extension used to test the
unmodeled-element-drop behavior above). Tests assert both field-level parse
correctness against known values read directly from these real files, and
that `Parse` -> `Serialize` -> `Parse` again reproduces the same modeled
data.

## License

MIT OR Apache-2.0.
