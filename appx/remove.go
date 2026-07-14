package appx

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/wim"
)

// ApplicationsPath and DeprovisionedPath are the SOFTWARE-hive registry
// paths (relative to the hive root) backing an image's provisioned-package
// state, confirmed present and populated on a real, un-booted Windows 11
// 23H2 image (2026-07-14, via hivexregedit --export - see appx.go's doc
// comment). Applications holds one subkey per provisioned package
// (ApplicationsPath\<PackageFullName>, with nested subkeys for dependency
// packages); Deprovisioned holds one empty marker subkey per blocked
// package family (see the "Keep removed apps from returning during an
// update" mechanism cited in TODO.md - note that, unlike that document's
// own paraphrase, the real marker subkey observed is named exactly the
// package's PackageFamilyName, i.e. "<name>_<PublisherID>", not
// "<PackageFamilyName>_<PublisherId>").
const (
	ApplicationsPath  = `Microsoft\Windows\CurrentVersion\Appx\AppxAllUserStore\Applications`
	DeprovisionedPath = `Microsoft\Windows\CurrentVersion\Appx\AppxAllUserStore\Deprovisioned`
)

// windowsAppsPath is the image-relative path containing one directory per
// installed/provisioned package, keyed by PackageFullName.
const windowsAppsPath = `Program Files\WindowsApps`

// FamilyNameFromFullName derives a package's family name ("<name>_<publisher
// ID>") from its full name ("<name>_<version>_<architecture>_<resourceId>_
// <publisherID>", e.g.
// "Microsoft.Paint_11.2201.22.0_x64__8wekyb3d8bbwe"). A package Name may not
// itself contain an underscore (see the "Package identity" Microsoft Learn
// documentation cited in appx.go), so splitting on "_" always yields exactly
// five components.
func FamilyNameFromFullName(fullName string) (string, error) {
	parts := strings.Split(fullName, "_")
	if len(parts) != 5 {
		return "", fmt.Errorf("appx: full name %q: expected 5 underscore-separated components (name_version_architecture_resourceid_publisherid), got %d", fullName, len(parts))
	}
	return parts[0] + "_" + parts[4], nil
}

// RemoveProvisioned removes every <Provisioned><Package> entry in pl whose
// family name (see FamilyNameFromFullName) equals familyName - the target
// package itself plus its bundle/resource/dependency siblings, which share
// the same family and differ only in Version/Architecture/ResourceId (see
// TODO.md's real-data survey of AppxProvisioning.xml's shape). It returns
// the removed entries' FullName values, which Remove uses to also clean up
// their WindowsApps folders and Applications registry subkeys.
//
// If addEndOfLife, familyName is also appended to pl.EndOfLife (skipped if
// already present), per the documented "keep removed apps from returning
// during an update" mechanism (see ApplicationsPath's doc comment).
func RemoveProvisioned(pl *ProvisionList, familyName string, addEndOfLife bool) []string {
	var removed []string
	kept := pl.Provisioned[:0]
	for _, p := range pl.Provisioned {
		if fam, err := FamilyNameFromFullName(p.FullName); err == nil && fam == familyName {
			removed = append(removed, p.FullName)
			continue
		}
		kept = append(kept, p)
	}
	pl.Provisioned = kept

	if addEndOfLife {
		for _, e := range pl.EndOfLife {
			if e.FamilyName == familyName {
				return removed
			}
		}
		pl.EndOfLife = append(pl.EndOfLife, EndOfLifePackage{FamilyName: familyName})
	}
	return removed
}

// Remove performs full offline removal of a provisioned package family from
// an un-booted factory image (see the package doc comment's "explicit
// non-goals" for why this is scoped to that case, not a live-captured
// image):
//
//  1. Removes familyName's <Provisioned> entries from pl (see
//     RemoveProvisioned), optionally adding familyName to <EndOfLife>.
//  2. Deletes each removed entry's ApplicationsPath\<FullName> subkey from
//     applications (see ApplicationsPath's doc comment).
//  3. Adds a DeprovisionedPath\<familyName> marker subkey under
//     deprovisioned (see DeprovisionedPath's doc comment).
//  4. Removes each removed entry's windowsAppsPath\<FullName> directory
//     from root's tree, decrementing bt's blob-table refcounts for every
//     stream under it first (mirroring the sibling driver package's
//     Uninstall/decrementBlobRefs - never reclaiming a zero-RefCount entry,
//     since a whole-WIM-aware higher-level caller owns that decision).
//
// A removed entry with no corresponding WindowsApps folder or Applications
// subkey (already removed, or a resource package that never had a
// registry entry of its own) is not an error - each step treats "already
// gone" as success, matching the sibling driver package's Uninstall
// convention.
//
// applications, deprovisioned, root, and bt may all be nil, independently,
// to skip the corresponding step(s) - e.g. a caller that only wants pl
// mutated, or that hasn't loaded the SOFTWARE hive or image tree at all.
func Remove(pl *ProvisionList, familyName string, addEndOfLife bool, applications, deprovisioned *regf.Key, root *wim.DirEntry, bt *wim.BlobTable) error {
	if pl == nil {
		return errors.New("appx: remove: nil provision list")
	}
	if familyName == "" {
		return errors.New("appx: remove: no family name given")
	}

	removedNames := RemoveProvisioned(pl, familyName, addEndOfLife)

	if applications != nil {
		for _, name := range removedNames {
			applications.DeleteSubkey(name)
		}
	}

	if deprovisioned != nil {
		deprovisioned.FindOrCreateSubkey(familyName)
	}

	if root != nil {
		for _, name := range removedNames {
			path := windowsAppsPath + `\` + name
			entry, err := root.Lookup(path)
			if err != nil {
				if errors.Is(err, wim.ErrNotFound) {
					continue
				}
				return fmt.Errorf("appx: remove %q: %w", name, err)
			}
			decrementBlobRefs(bt, entry)
			if err := root.Remove(path); err != nil && !errors.Is(err, wim.ErrNotFound) {
				return fmt.Errorf("appx: remove %q: %w", name, err)
			}
		}
	}

	return nil
}

// decrementBlobRefs walks removed and its full subtree, decrementing, in
// bt, the RefCount of every blob-table entry whose Hash matches one of
// removed's streams - a direct copy of the sibling driver package's own
// decrementBlobRefs (driver/uninstall.go), duplicated rather than imported
// per this project's small-helper-duplication convention (see
// registry/registry.go's NewBlob for the same rationale). See that
// function's doc comment for the full never-reclaim/never-underflow
// reasoning.
func decrementBlobRefs(bt *wim.BlobTable, removed *wim.DirEntry) {
	if bt == nil || removed == nil {
		return
	}
	index := make(map[wim.Hash]*wim.BlobDescriptor, len(bt.Entries))
	for i := range bt.Entries {
		index[bt.Entries[i].Hash] = &bt.Entries[i]
	}

	var zero wim.Hash
	var walk func(d *wim.DirEntry)
	walk = func(d *wim.DirEntry) {
		for _, s := range d.Streams {
			if s.Hash == zero {
				continue
			}
			if desc, ok := index[s.Hash]; ok && desc.RefCount > 0 {
				desc.RefCount--
			}
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	walk(removed)
}
