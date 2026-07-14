package component

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/wim"
)

// WinSxSDir is the standard offline-image directory holding each
// component's own payload files, one subdirectory per component-level
// identity (named exactly like its `.manifest` file, minus the extension).
const WinSxSDir = `Windows\WinSxS`

// Remove deletes e's on-disk files from root's tree as a best-effort
// fallback, per this repo's TODO.md "CBS/servicing package subsystem"
// research verdict: the `COMPONENTS` hive's internal schema is undocumented
// and not safely mutable, so Remove only ever touches plain files/
// directories, leaving that hive untouched and, by design, inconsistent
// with the image's actual file layout afterward - a documented, permanent
// limitation, not an oversight (mirroring the sibling `driver` package's
// own DriverStore-hash non-goal precedent).
//
// For a KindPackage entry (a `servicing\Packages\*.mum` file), Remove
// deletes that `.mum` file and its paired `.cat` catalog of the same base
// name - confirmed, not assumed, to always exist together 1:1 by name in a
// real image (2026-07-14: every one of 1266 real `.mum` files in a real
// Windows 11 23H2 image's `servicing\Packages` has an exact same-base-name
// `.cat`, and vice versa, with zero exceptions).
//
// For a KindComponent entry (a `WinSxS\Manifests\*.manifest` file), Remove
// deletes that `.manifest` file and, if present, the WinSxSDir\<base-name>
// payload directory of the same base name. That payload directory is
// treated as optional, not required: confirmed 2026-07-14 that 4216 of
// 17189 real `.manifest` files in the same real image have no
// corresponding WinSxS payload directory at all (these are policy/
// metadata-only assemblies with nothing else to delete), so its absence is
// simply not an error, the same as any other already-removed path Remove
// encounters.
//
// Every individual file/directory Remove is asked to delete is treated
// this way: if it does not exist, that step is a no-op, not an error -
// Remove is meant to be safely callable against a target that may have
// already been partially cleaned up. bt may be nil (blob refcounts are
// then left untouched) or the image's *wim.BlobTable, in which case every
// removed subtree's stream hashes have their RefCount decremented by one,
// mirroring the sibling `driver`/`appx` packages' own Uninstall/Remove
// (never reclaiming a zero-RefCount entry itself, and never letting one
// underflow past 0 - both whole-WIM-aware concerns left to a higher-level
// caller).
func Remove(root *wim.DirEntry, bt *wim.BlobTable, e *Entry) error {
	if root == nil {
		return errors.New("component: remove: nil root directory entry")
	}
	if e == nil {
		return errors.New("component: remove: nil entry")
	}

	switch e.Kind {
	case KindPackage:
		base := trimSuffixFold(e.FileName, ".mum")
		if err := removePath(root, bt, PackagesDir+`\`+e.FileName); err != nil {
			return err
		}
		if err := removePath(root, bt, PackagesDir+`\`+base+".cat"); err != nil {
			return err
		}
	case KindComponent:
		if err := removePath(root, bt, ManifestsDir+`\`+e.FileName); err != nil {
			return err
		}
		base := trimSuffixFold(e.FileName, ".manifest")
		if err := removePath(root, bt, WinSxSDir+`\`+base); err != nil {
			return err
		}
	default:
		return fmt.Errorf("component: remove: unknown Kind %v", e.Kind)
	}
	return nil
}

// trimSuffixFold removes suffix from s if present, matching
// case-insensitively (real file names observed are consistently
// lowercase-extensioned, but this does not assume it).
func trimSuffixFold(s, suffix string) string {
	if len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s[:len(s)-len(suffix)]
	}
	return s
}

// removePath deletes path from root's tree, decrementing bt's blob
// refcounts for its entire subtree first if it exists, and treating
// path's absence as success rather than an error.
func removePath(root *wim.DirEntry, bt *wim.BlobTable, path string) error {
	entry, err := root.Lookup(path)
	if err != nil {
		if errors.Is(err, wim.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("component: remove %q: %w", path, err)
	}

	decrementBlobRefs(bt, entry)

	if err := root.Remove(path); err != nil && !errors.Is(err, wim.ErrNotFound) {
		return fmt.Errorf("component: remove %q: %w", path, err)
	}
	return nil
}

// decrementBlobRefs walks removed and its full subtree, decrementing, in
// bt, the RefCount of every blob-table entry whose Hash matches one of
// removed's streams - a direct copy of the sibling driver/appx packages'
// own decrementBlobRefs (driver/uninstall.go, appx/remove.go), duplicated
// rather than imported per this project's small-helper-duplication
// convention. See driver/uninstall.go's decrementBlobRefs doc comment for
// the full never-reclaim/never-underflow reasoning.
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
