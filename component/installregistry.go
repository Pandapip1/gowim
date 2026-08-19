// This file extends the component package to write the servicing-side
// registry bookkeeping an installed component needs in order for the image
// to remain serviceable, using the sibling regf package
// (github.com/Pandapip1/gowim/regf) purely as a plain Key/Value struct tree
// -- exactly as install.go builds *wim.DirEntry trees by hand, and exactly
// as the sibling driver package's registryinstall.go does for driver
// packages. Loading the hives out of an image and saving them back is the
// sibling registry package's job, not this one's.
//
// # Provenance of everything written here
//
// None of this schema is documented by Microsoft. Every value name, type and
// shape below was measured against one real Windows 11 image's own
// `COMPONENTS` and `SOFTWARE` hives (build 10.0.26200, SP build 8037, en-US,
// amd64; 28069 components, 3983 deployments, 2274 catalogs, 3517 packages,
// 16216 `Winners` keys), read with this repo's own regf parser. Where a
// statement below cites a count, that count is a measurement made while
// writing this file, not a recollection. Where something is inference, it
// says so.
//
// The one thing measurement cannot supply is the family of 16-hex-digit
// identity hashes CBS embeds in `WinSxS` keyforms, deployment key names, and
// truncated `f!`/`p!`/`s!`/`i!` value names. Those come from an undocumented
// SxS identity hash; the obvious digests do not reproduce them (see
// ComponentInstall.KeyForm). Every place one is needed, this package takes
// it from the caller and refuses to guess.
package component

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/mum"
	"github.com/Pandapip1/gowim/regf"
)

// Registry paths this package writes, relative to each hive's root key.
const (
	// ComponentsKeyPath is where the `COMPONENTS` hive records one subkey
	// per component-level manifest. The 1:1 invariant is exact in a real
	// image: 28069 `WinSxS\Manifests\*.manifest` files against 28069 subkeys
	// here, with byte-identical names (manifest file name minus
	// `.manifest`), zero entries on either side without a counterpart.
	ComponentsKeyPath = `DerivedData\Components`

	// DeploymentsKeyPath is where the `COMPONENTS` hive records deployment
	// units (3983 in the measured image). Every component links to at least
	// one via a `c!` value.
	DeploymentsKeyPath = `CanonicalData\Deployments`

	// CatalogsKeyPath is where the `COMPONENTS` hive records catalogs, one
	// subkey per `WinSxS\Catalogs\<hex>.cat` file, the key name being the
	// SHA-256 of the catalog's own bytes (2274 files against 2274 keys,
	// identical name sets).
	CatalogsKeyPath = `CanonicalData\Catalogs`

	// CBSPackagesKeyPath is where the `SOFTWARE` hive records installed
	// packages: 3517 subkeys against 3517 `servicing\Packages\*.mum` files.
	// This is the key that makes `DISM /Get-Packages` list a package.
	CBSPackagesKeyPath = `Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`

	// WinnersKeyPath is the runtime SxS activation index -- the one registry
	// location that is actually load-bearing at process start, and the only
	// one of these five outside the `COMPONENTS` hive. `sxs.dll` references
	// it by name; `wcp.dll` contains `WriteWinnersFromChangelist`, i.e. CBS
	// derives it from the component store. Microsoft was asked in Q&A
	// #296832 to document the `SideBySide` subkeys and declined, so this is
	// reverse-engineered knowledge with no schema to check against.
	WinnersKeyPath = `Microsoft\Windows\CurrentVersion\SideBySide\Winners`
)

// Defaults for PackageInstall's `SOFTWARE`-side values, chosen as the value a
// real image's package keys most often carry (see PackageInstall).
const (
	// DefaultInstallClient is the `InstallClient` string DISM itself writes.
	DefaultInstallClient = "DISM Package Manager Provider"
	// PackageStateInstalled is `CurrentState` 0x40, carried by 2071 of the
	// 3517 package keys in the measured image (the other 1446 carry 0x70).
	// The enumeration itself is undocumented; 0x40 is used here because it
	// is the modal value of a fully-installed package, which is inference
	// from the distribution rather than a decoded constant.
	PackageStateInstalled uint32 = 0x40
	// VisibilityDefault is `Visibility` 2, carried by 3319 of 3517.
	VisibilityDefault uint32 = 2
)

// Hives is the pair of already-loaded registry hive root keys InstallRegistry
// writes into. Both come from the sibling registry package's LoadHiveSet
// (registry.HiveComponents and registry.HiveSoftware), whose Hive.Save then
// writes the modified tree back into the image.
//
// The two are deliberately separate parameters rather than one image handle,
// because they serve genuinely disjoint purposes and a caller may legitimately
// have only one of them:
//
//   - Components carries the servicing store's own bookkeeping. Nothing at
//     runtime reads it. Omitting it (nil) is what makes an image unserviceable.
//   - Software carries the CBS package list (`DISM /Get-Packages`) and the
//     `SideBySide\Winners` activation index, which *is* read at process start
//     for `Type=win32`/`win32-policy` assemblies.
type Hives struct {
	Components *regf.Key
	Software   *regf.Key
}

// InstallRegistry writes inst's servicing bookkeeping into the two hives.
//
// It refuses to run for a BuildOnce installation (ErrBuildOnce) and for one
// that never chose (ErrServiceabilityUnset): the whole point of
// Serviceability is that "did we write the hive entries?" is answered
// explicitly rather than by whichever call site happened to run.
//
// Into hives.Components it writes, for each component:
//
//	DerivedData\Components\<KeyForm>
//	    identity  REG_BINARY  canonical identity string, ASCII, NOT NUL-terminated
//	    S256H     REG_BINARY  SHA-256 of the manifest XML (32 bytes)
//	    f!<file>  REG_DWORD   one per payload file
//	    c!<depl>  REG_BINARY  zero-length, one per Deployments entry
//	    CF        REG_DWORD   only if ComponentInstall.CF is non-nil
//
// for each deployment:
//
//	CanonicalData\Deployments\<KeyName>
//	    appid              REG_BINARY  ASCII, NOT NUL-terminated
//	    CatalogThumbprint  REG_SZ      64 hex chars, NUL-terminated
//
// and, for each package catalog, `CanonicalData\Catalogs\<sha256-hex>` with a
// `c!` back-link to each deployment that names that thumbprint.
//
// Into hives.Software it writes, for each package,
// `...\Component Based Servicing\Packages\<Name>` with `InstallName`,
// `InstallLocation`, `InstallClient`, `CurrentState`, `Visibility`,
// `SelfUpdate` and an `Owners` subkey.
//
// # What it deliberately does not write
//
//   - `p!`/`s!`/`i!` values on a deployment key, which link a deployment to
//     the CBS package(s) that own it. Their data format was decoded during
//     this pass (u32 string length, u32 flag 0 or 1, that many ASCII bytes,
//     then one extra byte only when the flag is 1) but their *value names*
//     embed one of the uncomputable 16-hex identity hashes -- `p!CBS_` plus
//     the package identity lowercased and cut to 60 characters, plus `_`,
//     plus 16 hex digits -- so gowim cannot form a correct name. Writing a
//     name with a wrong hash would be worse than writing nothing.
//   - `SideBySide\Winners` entries, unless the caller asks for them by
//     supplying a WinnersInstall (see InstallWinners), for the same reason:
//     that key name embeds a hash over the *version-less* identity, a
//     different hash from the keyform's (measured: the same component's
//     keyform ends `_87ebc5097a2f9e52` while its Winners key ends
//     `_62fe57338acfab7a`).
//   - Anything under `WinSxS\FileMaps`; see FileMapsDir.
//
// Like the sibling driver package's InstallRegistry, it is safe to call more
// than once with the same Installation: every write goes through
// find-or-create/set-value, so a second call updates rather than duplicates.
func InstallRegistry(hives *Hives, inst *Installation) error {
	if hives == nil {
		return errors.New("component: install registry: nil hives")
	}
	if inst == nil {
		return errors.New("component: install registry: nil installation")
	}
	switch inst.Serviceability {
	case ServiceabilityUnset:
		return ErrServiceabilityUnset
	case BuildOnce:
		return ErrBuildOnce
	case Serviceable:
	default:
		return fmt.Errorf("component: install registry: unknown Serviceability %v", inst.Serviceability)
	}
	if err := validate(inst); err != nil {
		return err
	}

	if len(inst.Components) > 0 || len(inst.Deployments) > 0 {
		if hives.Components == nil {
			return errors.New("component: install registry: Serviceable installation needs the COMPONENTS hive root key")
		}
	}
	if len(inst.Packages) > 0 && hives.Software == nil {
		return errors.New("component: install registry: Serviceable installation with packages needs the SOFTWARE hive root key")
	}

	if err := installComponentKeys(hives.Components, inst); err != nil {
		return err
	}
	if err := installDeploymentKeys(hives.Components, inst); err != nil {
		return err
	}
	installCatalogKeys(hives.Components, inst)
	return installPackageKeys(hives.Software, inst)
}

func installComponentKeys(componentsHive *regf.Key, inst *Installation) error {
	if len(inst.Components) == 0 {
		return nil
	}
	parent := componentsHive.FindOrCreatePath(ComponentsKeyPath)
	for i, c := range inst.Components {
		identity := c.Identity
		if identity == "" {
			m, err := mum.Parse(c.Manifest)
			if err != nil {
				return fmt.Errorf("component: install registry: components[%d] (%s): "+
					"no Identity supplied and its Manifest does not parse: %w", i, c.KeyForm, err)
			}
			identity = CanonicalIdentity(m.Identity)
		}

		key := parent.FindOrCreateSubkey(c.KeyForm)
		key.SetValue("identity", regf.RegBinary, []byte(identity))
		sum := ManifestHash(c.Manifest)
		key.SetValue("S256H", regf.RegBinary, sum[:])
		if c.CF != nil {
			key.SetValue("CF", regf.RegDWORD, regf.EncodeDWORD(*c.CF))
		}
		for _, d := range c.Deployments {
			key.SetValue("c!"+d, regf.RegBinary, nil)
		}
		for _, f := range c.Files {
			name, err := fileValueName(f.Name)
			if err != nil {
				return fmt.Errorf("component: install registry: components[%d] (%s): %w", i, c.KeyForm, err)
			}
			key.SetValue(name, regf.RegDWORD, regf.EncodeDWORD(fileFlagPresent))
		}
	}
	return nil
}

// fileFlagPresent is the `f!<file>` DWORD this package writes. Real images
// carry 1 and 0x41 (both observed on plain-manifest components in the
// measured image); the meaning of the individual bits is not established --
// `wcp.dll` leaks a `FileFlags` enumerator list (`StageMark |
// Hardlinked_DEPRECATED | DeltaCompressed_DEPRECATED |
// NTFSCompressed_DEPRECATED | LZMSCompressed | BackupCompressed |
// PSFXCompressedForwardReverseDelta | PSFXCompressedNullDelta`) but not the
// bit order. 1 is written here as the smaller, more conservative of the two
// observed values: it claims no compression or backup state for a file this
// package just wrote uncompressed. That choice is inference.
const fileFlagPresent uint32 = 1

// maxVerbatimFileValueName is the longest payload file name that appears
// verbatim in an `f!` value name. Beyond it CBS truncates to the first 25
// characters plus `_` plus a 16-hex identity hash, giving a 42-character
// name.
//
// This is measured, and the boundary is sharp. Decompressing all 28069
// manifests in the real image with this repo's own pa30 package (28069
// decoded, 0 failures) and pairing each manifest's `<file name=...>` values
// against its hive key's `f!` values: every file name of 25 characters or
// fewer that has an `f!` entry appears verbatim, and no file name of 26
// characters or more ever does. The truncated form is always exactly 42
// characters.
const maxVerbatimFileValueName = 25

// fileValueName returns the `f!<name>` value name for a payload file, or an
// error if CBS would have truncated it -- which gowim cannot reproduce,
// because the replacement suffix is one of the uncomputable identity hashes.
// Erroring is the honest outcome: writing `f!<43-character-name>` would
// produce a value no real image contains and that CBS's own lookups (which
// presumably form the truncated name themselves) would not find.
func fileValueName(name string) (string, error) {
	if len(name) > maxVerbatimFileValueName {
		return "", fmt.Errorf("payload file name %q is %d characters; CBS truncates `f!` value names "+
			"longer than %d characters to a 25-character prefix plus an undocumented 16-hex identity hash "+
			"that gowim cannot compute (measured across all 28069 components of a real image)",
			name, len(name), maxVerbatimFileValueName)
	}
	return "f!" + name, nil
}

func installDeploymentKeys(componentsHive *regf.Key, inst *Installation) error {
	if len(inst.Deployments) == 0 {
		return nil
	}
	parent := componentsHive.FindOrCreatePath(DeploymentsKeyPath)
	for _, d := range inst.Deployments {
		key := parent.FindOrCreateSubkey(d.KeyName)
		if d.AppID != "" {
			key.SetValue("appid", regf.RegBinary, []byte(d.AppID))
		}
		if d.CatalogThumbprint != "" {
			key.SetValue("CatalogThumbprint", regf.RegSZ, encodeSZZ(d.CatalogThumbprint))
		}
	}
	return nil
}

// installCatalogKeys creates one `CanonicalData\Catalogs\<sha256-hex>` key
// per catalog supplied with a package, back-linking it to every deployment
// that names that thumbprint -- the same `c!<deployment>` zero-length
// REG_BINARY shape a real image's catalog keys carry (5162 such links across
// 2274 catalogs, all of which resolve to an existing deployment key).
func installCatalogKeys(componentsHive *regf.Key, inst *Installation) {
	var thumbprints []string
	for _, p := range inst.Packages {
		if len(p.Catalog) > 0 {
			thumbprints = append(thumbprints, CatalogThumbprint(p.Catalog))
		}
	}
	if len(thumbprints) == 0 {
		return
	}
	parent := componentsHive.FindOrCreatePath(CatalogsKeyPath)
	for _, tp := range thumbprints {
		key := parent.FindOrCreateSubkey(tp)
		for _, d := range inst.Deployments {
			if strings.EqualFold(d.CatalogThumbprint, tp) {
				key.SetValue("c!"+d.KeyName, regf.RegBinary, nil)
			}
		}
	}
}

func installPackageKeys(softwareHive *regf.Key, inst *Installation) error {
	if len(inst.Packages) == 0 {
		return nil
	}
	parent := softwareHive.FindOrCreatePath(CBSPackagesKeyPath)
	for _, p := range inst.Packages {
		key := parent.FindOrCreateSubkey(p.Name)
		key.SetValue("InstallName", regf.RegSZ, encodeSZZ(p.Name+".mum"))
		key.SetValue("InstallLocation", regf.RegSZ, encodeSZZ(p.InstallLocation))
		client := p.InstallClient
		if client == "" {
			client = DefaultInstallClient
		}
		key.SetValue("InstallClient", regf.RegSZ, encodeSZZ(client))
		state := p.CurrentState
		if state == 0 {
			state = PackageStateInstalled
		}
		key.SetValue("CurrentState", regf.RegDWORD, regf.EncodeDWORD(state))
		vis := p.Visibility
		if vis == 0 {
			vis = VisibilityDefault
		}
		key.SetValue("Visibility", regf.RegDWORD, regf.EncodeDWORD(vis))
		key.SetValue("SelfUpdate", regf.RegDWORD, regf.EncodeDWORD(p.SelfUpdate))
		if len(p.Owners) > 0 {
			owners := key.FindOrCreateSubkey("Owners")
			for _, o := range p.Owners {
				owners.SetValue(o, regf.RegDWORD, regf.EncodeDWORD(0))
			}
		}
	}
	return nil
}

// WinnersInstall is one `SOFTWARE\...\SideBySide\Winners` entry: the runtime
// activation index a `Type=win32`/`win32-policy` assembly needs in order for
// any process to resolve it. Without it the component's files sit in
// `WinSxS` and nothing ever binds to them.
//
// The shape is measured (16216 such keys in the real image; the two dumped
// below are Common-Controls', amd64 and x86, and every Winners key inspected
// has this shape):
//
//	Winners\<versionless-keyform>\<major.minor>
//	    (default)          REG_SZ      the winning full version, e.g. "6.0.26100.8037"
//	    <full version>     REG_BINARY  one byte, 0x01
//
// KeyForm here is *not* the component's WinSxS keyform: it is the same
// identity with the version field removed and with a different trailing
// 16-hex hash (measured: `amd64_microsoft.windows.common-controls_
// 6595b64144ccf1df_5.82.26100.8037_none_87ebc5097a2f9e52` for the component
// against `amd64_microsoft.windows.common-controls_6595b64144ccf1df_none_
// 62fe57338acfab7a` for its Winners key). gowim cannot compute either hash,
// so the caller supplies the whole key name -- see ComponentInstall.KeyForm.
type WinnersInstall struct {
	// KeyForm is the version-less keyform, supplied by the caller.
	KeyForm string
	// VersionFamily is the `<major.minor>` subkey, e.g. "6.0".
	VersionFamily string
	// Version is the winning full version, e.g. "6.0.26100.8037".
	Version string
}

// InstallWinners writes SideBySide\Winners entries into the SOFTWARE hive.
//
// It is separate from InstallRegistry, and opt-in, because it is the one
// write in this package that a running system reads on every activation, and
// because getting the key name wrong is silently ineffective rather than
// loud. Callers installing a `Type=win32` or `win32-policy` assembly need
// it; callers installing an ordinary servicing component (the
// `versionScope=NonSxS` majority -- 25796 of the 27668 PA30-compressed
// components in the measured image) do not, and must not write it.
//
// The `Winners` subtree is undocumented by Microsoft's deliberate choice
// (Q&A #296832 asked; Microsoft declined), so nothing here can be checked
// against a schema -- only against real values, which is what was done.
func InstallWinners(softwareHive *regf.Key, winners []WinnersInstall) error {
	if softwareHive == nil {
		return errors.New("component: install winners: nil SOFTWARE hive root key")
	}
	for i, w := range winners {
		if w.KeyForm == "" || w.VersionFamily == "" || w.Version == "" {
			return fmt.Errorf("component: install winners: winners[%d] needs KeyForm, VersionFamily and Version", i)
		}
		if !strings.HasPrefix(w.Version, w.VersionFamily+".") && w.Version != w.VersionFamily {
			return fmt.Errorf("component: install winners: winners[%d]: Version %q is not in VersionFamily %q",
				i, w.Version, w.VersionFamily)
		}
		key := softwareHive.FindOrCreatePath(WinnersKeyPath).
			FindOrCreateSubkey(w.KeyForm).
			FindOrCreateSubkey(w.VersionFamily)
		key.SetValue("", regf.RegSZ, encodeSZZ(w.Version))
		key.SetValue(w.Version, regf.RegBinary, []byte{1})
	}
	return nil
}

// CanonicalIdentity formats an assembly identity the way the `COMPONENTS`
// hive's `identity` (and a deployment's `appid`) value spells it.
//
// The field order is fixed, and it is not alphabetical -- it is
// name, Culture, Type, Version, PublicKeyToken, ProcessorArchitecture,
// versionScope, with Type and versionScope present only when the identity
// has them. That is measured across all 28069 `identity` values in the real
// image, which use exactly four orderings, all of them this one sequence
// with those two fields present or absent:
//
//	24905  Culture Version PublicKeyToken ProcessorArchitecture versionScope
//	 1879  Culture Version PublicKeyToken ProcessorArchitecture
//	  891  Culture Type Version PublicKeyToken ProcessorArchitecture versionScope
//	  394  Culture Type Version PublicKeyToken ProcessorArchitecture
//
// A manifest's `language` attribute becomes `Culture`, and an absent
// language becomes `Culture=neutral` (measured: a component manifest with
// `language="en-US"` has `Culture=en-US` in its hive identity, and one with
// no language attribute has `Culture=neutral`).
//
// Note that `buildType` never appears in a canonical identity even though
// real manifests carry it -- also measured, in that no `identity` value in
// the image contains the substring.
func CanonicalIdentity(id mum.AssemblyIdentity) string {
	culture := id.Language
	if culture == "" {
		culture = "neutral"
	}
	var b strings.Builder
	b.WriteString(id.Name)
	b.WriteString(", Culture=")
	b.WriteString(culture)
	if id.Type != "" {
		b.WriteString(", Type=")
		b.WriteString(id.Type)
	}
	b.WriteString(", Version=")
	b.WriteString(id.Version)
	b.WriteString(", PublicKeyToken=")
	b.WriteString(id.PublicKeyToken)
	b.WriteString(", ProcessorArchitecture=")
	b.WriteString(id.ProcessorArchitecture)
	if id.VersionScope != "" {
		b.WriteString(", versionScope=")
		b.WriteString(canonicalVersionScope(id.VersionScope))
	}
	return b.String()
}

// canonicalVersionScope normalizes a manifest's `versionScope` spelling to
// the one the hive records.
//
// This is the one field CBS does *not* copy through verbatim, and it was
// found by a real-image test failing rather than by reading anything: real
// manifests spell the value three different ways -- `nonSxS` (67106
// occurrences), `nonSXS` (28) and `nonSxs` (12) -- while every one of the
// 25796 hive identities that carries the field spells it `NonSxS`. Cross-
// tabulating manifest attribute against hive field over all 28069
// components, `versionScope` is the *only* identity field with any
// normalization at all: name, version, publicKeyToken, processorArchitecture
// and type are byte-identical in both places, and `language` passes through
// unchanged too (`en-us` stays `en-us`; only an absent language becomes
// `Culture=neutral`).
//
// Only the one token was ever observed, so only the one token is
// normalized -- anything else is passed through unchanged rather than run
// through a guessed general capitalization rule, since there is no evidence
// for what such a rule would be.
func canonicalVersionScope(s string) string {
	if strings.EqualFold(s, "NonSxS") {
		return "NonSxS"
	}
	return s
}

// DeploymentKeyNamePrefix returns the leading, computable field of a
// `CanonicalData\Deployments` key name: the deployment's identity name
// capped at 24 characters, as the first 11 characters, `..`, and the last 11.
//
// Verified against every one of the 3983 deployment keys in the real image:
// splitting each key name on `_`, dropping its last three fields
// (publicKeyToken, version, hash) and comparing what remains against this
// function's output for that key's own `appid` name gives 3983 matches and
// zero mismatches. 3096 of the 3983 are actually truncated; the rest are
// short enough to pass through.
//
// It is exported because it is the one part of a deployment key name gowim
// can compute -- the trailing 16 hex digits are the uncomputable identity
// hash, so DeploymentInstall.KeyName still has to be supplied whole. This
// function is useful for cross-checking a supplied name against its appid.
func DeploymentKeyNamePrefix(identityName string) string {
	const max = 24
	if len(identityName) <= max {
		return identityName
	}
	const head = (max - 2) / 2
	return identityName[:head] + ".." + identityName[len(identityName)-(max-2-head):]
}

// encodeSZZ encodes s as NUL-terminated UTF-16LE REG_SZ data.
//
// regf.EncodeSZ deliberately does not terminate (see its doc comment), which
// is fine for values this project writes and reads back itself. Real hive
// values written by Windows *are* terminated -- measured here: a package
// key's `InstallName` for `<name>.mum` is 2*len+2 bytes ending `00 00`, and
// a deployment's 64-character `CatalogThumbprint` is 130 bytes, not 128. The
// values in this file are ones the servicing stack reads, so they match what
// it writes.
func encodeSZZ(s string) []byte {
	return append(regf.EncodeSZ(s), 0, 0)
}
