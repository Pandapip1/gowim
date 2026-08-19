package component

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/Pandapip1/gowim/wim"
)

// CatalogsDir is the offline-image directory holding the component store's
// own catalogs, one `<sha256-hex>.cat` file per catalog. Confirmed 1:1 with
// the `COMPONENTS` hive's `CanonicalData\Catalogs\<sha256-hex>` subkeys in a
// real image, with the key/file name being the SHA-256 of the catalog file's
// own bytes (2274 of 2274, zero exceptions -- see TODO.md's
// "Component-installation research pass", Q2).
const CatalogsDir = `Windows\WinSxS\Catalogs`

// NewBlob is one installed file's content that was not already present in
// the blob table, paired with the hash the caller should use to place it
// when finally assembling an output WIM file. Mirrors the sibling driver and
// registry packages' own NewBlob types -- duplicated rather than imported,
// matching this project's convention of small sibling-package type
// duplication over a cross-cutting dependency (see driver/install.go and
// registry/registry.go's own such notes).
type NewBlob struct {
	Hash wim.Hash
	Data []byte
}

// Serviceability is the caller's explicit declaration of what the image
// being built is for. It has no usable zero value: Install and
// InstallRegistry both reject ServiceabilityUnset, because the choice
// between the two real values is a genuine correctness decision the research
// pass behind this package (TODO.md, "Component-installation research pass",
// Q3) established cannot be defaulted safely in either direction.
//
// The two differ in exactly one thing -- whether the servicing registry
// bookkeeping is written -- and the evidence for why that matters is:
//
//   - Nothing at *runtime* reads the `COMPONENTS` hive. A component whose
//     files are placed correctly works: its payload files are ordinary
//     files. That is a measurement, not an assumption -- `ntoskrnl.exe`,
//     `smss.exe`, `csrss.exe`, `winsrv.dll`, `kernel32.dll`, `drvstore.dll`,
//     `ntdll.dll`, `sxs.dll` and `sxsstore.dll` in a real image contain zero
//     references to `\Registry\Machine\COMPONENTS`; only `wcp.dll`, the
//     servicing stack, does.
//   - But an entry-less component is *not* merely invisible to servicing.
//     CheckSUR and `DISM /ScanHealth` emit a named finding for exactly this
//     condition, `CSI Missing Winning Component Key`, with the WinSxS
//     keyform as its argument, and there is at least one reported case of
//     updates failing until the missing `DerivedData\Components` keys were
//     restored. That is third-party-hosted Microsoft tool output rather than
//     Microsoft documentation -- graded (ii) in the research write-up -- but
//     it is the only direct evidence either way, and it points one way.
//
// So: BuildOnce is a defensible, documented tradeoff for an image that will
// be deployed and never updated (this project's own nano11-style use case).
// It is not a free one, and it is not symmetric with Remove's
// deliberately-hive-free design.
type Serviceability int

const (
	// ServiceabilityUnset is the zero value and is always an error. See
	// Serviceability's doc comment for why this type has no default.
	ServiceabilityUnset Serviceability = iota

	// BuildOnce writes only files: the manifest, the payload, the `.mum`
	// and the catalogs. No registry hive is touched, and InstallRegistry
	// refuses to run (ErrBuildOnce). The resulting image boots and the
	// component works, but the image is **not serviceable**: a later
	// CheckSUR/`DISM /ScanHealth` will name the orphan and a later update
	// may fail on it. Choose this only for an image that will never be
	// updated.
	BuildOnce

	// Serviceable additionally requires the caller to run InstallRegistry
	// against the image's `COMPONENTS` and `SOFTWARE` hives, so the
	// file-side and registry-side halves of the store agree. Install itself
	// still only places files -- it has no access to the hives, exactly as
	// the sibling driver package splits Install from InstallRegistry -- but
	// this value records that the second half is required, and
	// InstallRegistry rejects anything else.
	Serviceable
)

func (s Serviceability) String() string {
	switch s {
	case ServiceabilityUnset:
		return "unset"
	case BuildOnce:
		return "build-once"
	case Serviceable:
		return "serviceable"
	default:
		return fmt.Sprintf("Serviceability(%d)", int(s))
	}
}

// ErrBuildOnce is returned by InstallRegistry when the Installation declares
// Serviceability == BuildOnce. It is not a failure: it is the API refusing
// to half-do the thing the caller explicitly opted out of.
var ErrBuildOnce = errors.New("component: installation is BuildOnce; registry bookkeeping deliberately not written")

// ErrServiceabilityUnset is returned when an Installation leaves
// Serviceability at its zero value. See Serviceability's doc comment.
var ErrServiceabilityUnset = errors.New("component: Installation.Serviceability must be set to BuildOnce or Serviceable")

// PayloadFile is one file belonging to a component.
//
// Name is the file's name both inside the component's own
// `WinSxS\<KeyForm>\` store directory and at each of DestDirs. In a real
// image the component-store copy and the destination copy are the same
// content reached through two hardlinks; the offline-WIM analogue is two
// directory entries referencing one blob hash with the refcount bumped,
// which is what Install produces (the same thing driver.Install already does
// for driver payload files).
//
// DestDirs is the set of image-relative directories the file is *also*
// projected into, e.g. `Windows\System32`. It is this projection, not the
// manifest, that makes a component actually do anything. It may be empty:
// plenty of real components (every `Type=win32` SxS assembly, for instance)
// have payload only in their own store directory, resolved at runtime
// through activation contexts rather than by being present in System32.
//
// Note that gowim does not derive DestDirs from the manifest itself. The
// sibling `mum` package models a manifest's identity, package and dependency
// vocabulary but not its `<file>` elements, so the `destinationPath`
// attribute (`$(runtime.system32)` and friends) is simply not available to
// this package yet -- the caller supplies the resolved directories, in the
// same spirit as driver.Install's destDirs parameter.
type PayloadFile struct {
	Name     string
	Data     []byte
	DestDirs []string
}

// ComponentInstall describes one component-level assembly to install.
type ComponentInstall struct {
	// KeyForm is the WinSxS "keyform": the `.manifest` file's name minus the
	// extension, the `WinSxS\<...>` payload directory's name, and the
	// `COMPONENTS\DerivedData\Components\<...>` key name, all of which are
	// byte-identical in a real image (28069 manifests vs 28069 hive keys,
	// zero entries on either side without a counterpart).
	//
	// **The caller must supply this; gowim cannot compute it.** A keyform
	// ends in a 16-hex-digit hash over the assembly identity produced by
	// Windows' own (undocumented) SxS identity hash. This pass tried the
	// obvious candidates against a real image's values -- MD5/SHA-1/SHA-256/
	// SHA-512 of the identity string in ASCII and UTF-16LE, with and without
	// a NUL terminator, in original/lower/upper case, taking the first or
	// last 8 bytes in either byte order -- and none of them reproduces it.
	// So it is unresolved, not merely unimplemented. For a component derived
	// from an existing one, copy the existing keyform; for a genuinely new
	// identity, the hash has to come from Windows.
	KeyForm string

	// Manifest is the component manifest, as **plain, uncompressed UTF-8
	// XML**. It is written to `WinSxS\Manifests\<KeyForm>.manifest`
	// verbatim, with no PA30/`DCM\x01` layer.
	//
	// That is deliberate and it is safe. `wcp.dll`'s
	// `Windows::WCP::Rtl::GetCompressedFileType` classifies a manifest
	// purely from its own first four bytes (`'D'`, `'C'`, a type byte,
	// `0x01`), and `DecompressManifest` treats "not compressed" as a success
	// path that returns the buffer untouched -- provenance never enters the
	// decision. Independently, the servicing stack is documented (in a
	// third-party CBS write-up) to *deliberately exclude* certain manifests,
	// notably Windows Common Controls, from compression, and a real image
	// contains 401 such plain manifests out of 28069. See TODO.md's Q1 for
	// the full evidence. A PA30 *encoder* is therefore not needed here.
	Manifest []byte

	// Identity is the canonical identity string written to the hive's
	// `identity` value in the Serviceable case, e.g.
	// "Microsoft.Windows.Common-Controls, Culture=neutral, Type=win32,
	// Version=5.82.26100.8037, PublicKeyToken=6595b64144ccf1df,
	// ProcessorArchitecture=amd64". If empty, InstallRegistry derives it
	// from the parsed Manifest via CanonicalIdentity.
	Identity string

	// Files is the component's payload. It may be empty: 6894 of the 28069
	// components in a real image have no `WinSxS\<keyform>` payload
	// directory at all (policy and metadata-only assemblies), and Install
	// creates no such directory when Files is empty, matching that shape.
	Files []PayloadFile

	// Deployments lists the `CanonicalData\Deployments` key names this
	// component belongs to, written as the hive's `c!<name>` back-link
	// values. Every one of the 28069 components in a real image has at least
	// one (46269 such links in total, all of which resolve to an existing
	// deployment key), so leaving this empty produces a shape that does not
	// occur naturally -- InstallRegistry allows it but the resulting entry
	// is incomplete. See DeploymentInstall for how the name is formed and
	// what part of it gowim cannot compute.
	Deployments []string

	// CF, if non-nil, is written as the hive's `CF` (ComponentFlags) DWORD.
	// The enumerator names are verbatim from `wcp.dll`'s own assert string
	// (`ComponentSparsed | CorruptionsDetected |
	// ClosureFlag_ManifestsPresent | ClosureFlag_FilesPresent |
	// DeltaCompressed_DEPRECATED | NTFSCompressed_DEPRECATED |
	// PayloadDeleted | ComponentHasMutableFile | BackupCandidate |
	// LZMSCompressed | UnlinkedFromDriverStore | BackupLZMSCompressed`) but
	// their *bit positions* are inference, corroborated only by the
	// PayloadDeleted == 0x40 correlation (3791 of 3791 components with that
	// bit set have no payload directory). It is sparse in real images --
	// 17127 of 28069 components carry no `CF` value at all -- so nil, the
	// default, is a perfectly normal shape.
	CF *uint32
}

// DeploymentInstall describes one `COMPONENTS\CanonicalData\Deployments`
// entry.
type DeploymentInstall struct {
	// KeyName is the deployment key's name. gowim can compute its first
	// three fields but not its last: the name is
	// `<truncated-appid-name>_<publicKeyToken>_<version>_<16-hex>`, where
	// the leading field is the identity name capped at 24 characters as
	// first-11 + ".." + last-11 (verified against all 3983 deployments in a
	// real image, zero mismatches -- see DeploymentKeyName, which implements
	// exactly that), and the trailing 16 hex digits are the same
	// uncomputable SxS identity hash discussed on ComponentInstall.KeyForm.
	// Supply the whole name.
	KeyName string

	// AppID is the `appid` value: the deployment's canonical identity
	// string, stored as REG_BINARY ASCII with no NUL terminator (28069 of
	// 28069 `identity` and 3983 of 3983 `appid` values in a real image are
	// unterminated).
	AppID string

	// CatalogThumbprint is the lowercase hex SHA-256 of the catalog that
	// covers this deployment, written as a NUL-terminated REG_SZ. It is the
	// same digest that names the `CanonicalData\Catalogs\<hex>` key and the
	// `WinSxS\Catalogs\<hex>.cat` file, so Install and InstallRegistry can
	// and do cross-check it against any catalog bytes supplied alongside.
	CatalogThumbprint string
}

// PackageInstall describes one package-level `.mum`/`.cat` pair and its
// `SOFTWARE` hive registration.
type PackageInstall struct {
	// Name is the package's name without extension. It is simultaneously the
	// `.mum` and `.cat` file's base name in `Windows\servicing\Packages` and
	// the `SOFTWARE\...\Component Based Servicing\Packages\<name>` key name
	// (3517 `.mum` files vs 3517 such keys in a real image, and every one of
	// those keys' `InstallName` value is exactly `<key name>.mum`).
	Name string

	// MUM is the package manifest's plain-XML bytes, written to
	// `Windows\servicing\Packages\<Name>.mum`.
	MUM []byte

	// Catalog, if non-empty, is written both to
	// `Windows\servicing\Packages\<Name>.cat` and to
	// `WinSxS\Catalogs\<sha256-hex>.cat`. Both placements are real: in the
	// measured image 2199 of the 2274 `WinSxS\Catalogs` files are
	// byte-identical to a `servicing\Packages\*.cat`.
	//
	// A caveat that is not gowim's to fix: a third-party catalog will not
	// chain to a Microsoft root, so the component can never be validated by
	// CBS, and on a system enforcing component or driver signing the payload
	// may be rejected on its own terms regardless of anything written here.
	Catalog []byte

	// InstallClient, CurrentState, Visibility and SelfUpdate are the
	// `SOFTWARE` package key's values. Zero values are replaced by the
	// defaults DefaultInstallClient / PackageStateInstalled /
	// VisibilityDefault / 0, which are the values a real image's package
	// keys overwhelmingly carry (`CurrentState` is 0x40 for 2071 and 0x70
	// for 1446 of 3517; `Visibility` is 2 for 3319 and 1 for 198;
	// `SelfUpdate` is 0 for 3513).
	InstallClient string
	CurrentState  uint32
	Visibility    uint32
	SelfUpdate    uint32

	// InstallLocation is written verbatim if non-empty. Real images carry
	// the build machine's staging path here (e.g. a `\\?\D:\wd\...` UNC
	// path), which is build provenance rather than anything consulted later,
	// so it is optional.
	InstallLocation string

	// Owners lists parent package names, written as DWORD values under the
	// package key's `Owners` subkey, matching the shape a real image uses.
	Owners []string
}

// Installation is one complete component-installation request: the unit
// Install and InstallRegistry both operate on, so the two halves cannot
// drift apart.
type Installation struct {
	// Serviceability is required; see its type's doc comment.
	Serviceability Serviceability

	Components  []ComponentInstall
	Deployments []DeploymentInstall
	Packages    []PackageInstall
}

// Install places inst's files into md's directory-entry tree and extends bt
// with any blob whose hash is not already present, deduplicating by hash (an
// existing entry's RefCount is incremented rather than a duplicate blob
// being added) -- the same contract, and the same return shape, as the
// sibling driver package's Install.
//
// For each component it writes:
//
//   - `Windows\WinSxS\Manifests\<KeyForm>.manifest`, the plain-XML manifest.
//   - `Windows\WinSxS\<KeyForm>\<file>` for each payload file, if any.
//   - `<dest>\<file>` for each of that file's DestDirs, sharing the store
//     copy's blob.
//
// For each package it writes `Windows\servicing\Packages\<Name>.mum`, and,
// if a Catalog is supplied, `Windows\servicing\Packages\<Name>.cat` plus
// `Windows\WinSxS\Catalogs\<sha256-hex>.cat`.
//
// It returns the (mutated) image metadata's root directory entry and the
// list of genuinely new blobs the caller must place in the eventual output
// WIM file.
//
// # What Install does not do
//
//   - It does not touch any registry hive. For a Serviceable installation
//     the caller must also call InstallRegistry against the image's
//     `COMPONENTS` and `SOFTWARE` hives; Install cannot do it itself because
//     it has no access to them, exactly as driver.Install and
//     driver.InstallRegistry are separate for the same reason.
//   - It does not update `Windows\WinSxS\FileMaps\*.cdf-ms`. See
//     ErrFileMapsNotUpdated and FileMapsDir for what is and is not known
//     about that gap.
func Install(md *wim.ImageMetadata, bt *wim.BlobTable, inst *Installation) (*wim.DirEntry, []NewBlob, error) {
	if md == nil {
		return nil, nil, errors.New("component: install: nil image metadata")
	}
	if bt == nil {
		return nil, nil, errors.New("component: install: nil blob table")
	}
	if inst == nil {
		return nil, nil, errors.New("component: install: nil installation")
	}
	if inst.Serviceability == ServiceabilityUnset {
		return nil, nil, ErrServiceabilityUnset
	}
	if md.Root == nil {
		md.Root = &wim.DirEntry{
			Attributes: wim.FileAttributeDirectory,
			SecurityID: wim.SecurityIDNone,
		}
	}

	if err := validate(inst); err != nil {
		return nil, nil, err
	}

	p := &placer{
		root:     md.Root,
		bt:       bt,
		existing: make(map[wim.Hash]*wim.BlobDescriptor, len(bt.Entries)),
	}
	for i := range bt.Entries {
		p.existing[bt.Entries[i].Hash] = &bt.Entries[i]
	}

	for _, c := range inst.Components {
		if err := p.place(ManifestsDir+`\`+c.KeyForm+".manifest", c.Manifest); err != nil {
			return nil, nil, err
		}
		for _, f := range c.Files {
			if err := p.place(WinSxSDir+`\`+c.KeyForm+`\`+f.Name, f.Data); err != nil {
				return nil, nil, err
			}
			for _, d := range f.DestDirs {
				if err := p.place(strings.TrimRight(normalizeSep(d), `\`)+`\`+f.Name, f.Data); err != nil {
					return nil, nil, err
				}
			}
		}
	}

	for _, pkg := range inst.Packages {
		if err := p.place(PackagesDir+`\`+pkg.Name+".mum", pkg.MUM); err != nil {
			return nil, nil, err
		}
		if len(pkg.Catalog) > 0 {
			if err := p.place(PackagesDir+`\`+pkg.Name+".cat", pkg.Catalog); err != nil {
				return nil, nil, err
			}
			if err := p.place(CatalogsDir+`\`+CatalogThumbprint(pkg.Catalog)+".cat", pkg.Catalog); err != nil {
				return nil, nil, err
			}
		}
	}

	return md.Root, p.newBlobs, nil
}

// CatalogThumbprint returns the lowercase hex SHA-256 of a catalog's bytes:
// the name a real image gives that catalog both as
// `WinSxS\Catalogs\<hex>.cat` and as the `COMPONENTS` hive's
// `CanonicalData\Catalogs\<hex>` key, and the value a deployment's
// `CatalogThumbprint` carries. Measured, not assumed: all 2274 catalog
// files/keys in a real image match this on both sides.
func CatalogThumbprint(catalog []byte) string {
	sum := sha256.Sum256(catalog)
	return hex.EncodeToString(sum[:])
}

// ManifestHash returns the SHA-256 of a manifest's XML content: the value a
// component's `S256H` carries. For the plain manifests this package writes,
// content and on-disk bytes are the same thing, which was re-verified during
// this implementation against all 401 plain manifests in a real image --
// every one of their `DerivedData\Components\<keyform>` `S256H` values
// equals the SHA-256 of the raw file, with zero exceptions.
func ManifestHash(manifest []byte) [32]byte { return sha256.Sum256(manifest) }

// validate checks the parts of an Installation that are wrong regardless of
// which half (files or registry) is about to act on it, so Install and
// InstallRegistry reject the same inputs the same way.
func validate(inst *Installation) error {
	for i, c := range inst.Components {
		if c.KeyForm == "" {
			return fmt.Errorf("component: install: components[%d] has no KeyForm", i)
		}
		if strings.ContainsAny(c.KeyForm, `\/`) {
			return fmt.Errorf("component: install: components[%d] KeyForm %q contains a path separator", i, c.KeyForm)
		}
		if len(c.Manifest) == 0 {
			return fmt.Errorf("component: install: components[%d] (%s) has no Manifest", i, c.KeyForm)
		}
		if len(c.Manifest) >= 2 && c.Manifest[0] == 'D' && c.Manifest[1] == 'C' {
			return fmt.Errorf("component: install: components[%d] (%s): Manifest looks PA30/DCM-compressed; "+
				"Install writes plain XML manifests and does not encode PA30", i, c.KeyForm)
		}
		for j, f := range c.Files {
			if f.Name == "" {
				return fmt.Errorf("component: install: components[%d] (%s) files[%d] has no Name", i, c.KeyForm, j)
			}
			if strings.ContainsAny(f.Name, `\/`) {
				return fmt.Errorf("component: install: components[%d] (%s) files[%d] Name %q contains a path separator",
					i, c.KeyForm, j, f.Name)
			}
		}
	}
	for i, d := range inst.Deployments {
		if d.KeyName == "" {
			return fmt.Errorf("component: install: deployments[%d] has no KeyName", i)
		}
		if d.CatalogThumbprint != "" {
			if _, err := hex.DecodeString(d.CatalogThumbprint); err != nil || len(d.CatalogThumbprint) != 64 {
				return fmt.Errorf("component: install: deployments[%d] (%s) CatalogThumbprint %q is not a 64-character hex SHA-256",
					i, d.KeyName, d.CatalogThumbprint)
			}
		}
	}
	for i, p := range inst.Packages {
		if p.Name == "" {
			return fmt.Errorf("component: install: packages[%d] has no Name", i)
		}
		if strings.ContainsAny(p.Name, `\/`) {
			return fmt.Errorf("component: install: packages[%d] Name %q contains a path separator", i, p.Name)
		}
		if len(p.MUM) == 0 {
			return fmt.Errorf("component: install: packages[%d] (%s) has no MUM", i, p.Name)
		}
	}
	return nil
}

// placer accumulates blob-table bookkeeping while writing files into a
// directory-entry tree, so one blob shared by a store copy and one or more
// projected destination copies is added once and refcounted per placement --
// the offline-WIM analogue of the hardlinks CBS uses for exactly this.
type placer struct {
	root     *wim.DirEntry
	bt       *wim.BlobTable
	existing map[wim.Hash]*wim.BlobDescriptor
	newBlobs []NewBlob
}

func (p *placer) place(path string, data []byte) error {
	hash := wim.Hash(sha1.Sum(data))

	// If something is already at this path, account for it before
	// overwriting, so calling Install twice with the same Installation does
	// not inflate refcounts (the "safe to call more than once" property the
	// sibling driver package's InstallRegistry documents, extended here to
	// the blob table as well as the tree).
	if prev, err := p.root.Lookup(path); err == nil && !prev.IsDirectory() {
		prevHash := prev.MainHash()
		if prevHash == hash {
			return nil
		}
		if desc, ok := p.existing[prevHash]; ok && desc.RefCount > 0 {
			desc.RefCount--
		}
	} else if err != nil && !errors.Is(err, wim.ErrNotFound) {
		return fmt.Errorf("component: install %q: %w", path, err)
	}

	if desc, ok := p.existing[hash]; ok {
		desc.RefCount++
	} else {
		p.bt.Entries = append(p.bt.Entries, wim.BlobDescriptor{
			Hash:       hash,
			RefCount:   1,
			PartNumber: 1,
			Resource: wim.ResourceHeader{
				UncompressedSize: uint64(len(data)),
			},
		})
		p.existing[hash] = &p.bt.Entries[len(p.bt.Entries)-1]
		p.newBlobs = append(p.newBlobs, NewBlob{Hash: hash, Data: data})
	}
	return placeFile(p.root, pathComponents(path), hash)
}

// normalizeSep converts '/' separators to '\', so callers may write
// destination directories either way (the same courtesy driver.Install's
// destDirs extends).
func normalizeSep(p string) string { return strings.ReplaceAll(p, "/", `\`) }

// pathComponents splits an image-relative path into its non-empty
// components, accepting either separator.
func pathComponents(path string) []string {
	var out []string
	for _, c := range strings.Split(normalizeSep(path), `\`) {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// placeFile walks/creates directory entries for components[:len-1] under
// root, then creates or overwrites the leaf as a regular file whose unnamed
// stream has the given hash. A direct counterpart of driver/install.go's
// function of the same name, duplicated rather than imported per this
// project's small-helper-duplication convention.
func placeFile(root *wim.DirEntry, components []string, hash wim.Hash) error {
	if len(components) == 0 {
		return errors.New("component: install: empty destination path")
	}
	dir := root
	for _, name := range components[:len(components)-1] {
		child := findChildFold(dir, name)
		if child == nil {
			child = &wim.DirEntry{
				Attributes: wim.FileAttributeDirectory,
				SecurityID: wim.SecurityIDNone,
				Name:       stringToUTF16LE(name),
			}
			dir.Children = append(dir.Children, child)
		} else if !child.IsDirectory() {
			return fmt.Errorf("component: install: path component %q already exists as a non-directory", name)
		}
		dir = child
	}

	leafName := components[len(components)-1]
	if leaf := findChildFold(dir, leafName); leaf != nil {
		leaf.Streams = []wim.Stream{{Hash: hash}}
		return nil
	}
	dir.Children = append(dir.Children, &wim.DirEntry{
		SecurityID: wim.SecurityIDNone,
		Name:       stringToUTF16LE(leafName),
		Streams:    []wim.Stream{{Hash: hash}},
	})
	return nil
}

// findChildFold looks up a direct child of dir by name, matching Windows'
// case-insensitive namespace.
func findChildFold(dir *wim.DirEntry, name string) *wim.DirEntry {
	for _, c := range dir.Children {
		if strings.EqualFold(c.NameUTF8(), name) {
			return c
		}
	}
	return nil
}

// stringToUTF16LE encodes a Go string as UTF-16LE bytes (no BOM, no
// terminator), matching the encoding wim.DirEntry.Name expects. wim's own
// helper of the same shape is unexported, so it is duplicated here the same
// way driver/install.go and cat/der.go duplicate it.
func stringToUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		out[i*2] = byte(c)
		out[i*2+1] = byte(c >> 8)
	}
	return out
}
