package driver

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/service"
	"github.com/Pandapip1/gowim/wim"
)

// Uninstall reverses what Install and InstallRegistry set up for a driver
// package already present in an image: it detaches the package's
// DriverStore folder (driverStoreDirName, a direct child of
// driverStoreParent - e.g. the Name of one of ListInstalled's returned
// InstalledPackages) from the directory-entry tree, deletes its
// Services\<serviceName> registry key under currentControlSet, and removes
// just its own Control\CriticalDeviceDatabase entries (the ones whose
// "Service" value equals serviceName), leaving any other driver's entries
// alone - i.e. the reverse of Install's file placement plus
// InstallRegistry's Services/CriticalDeviceDatabase merge.
//
// Unlike Install/InstallRegistry, Uninstall does not take a *Package: a
// driver already installed on a target image is not necessarily one the
// caller still has the original source files for (see the package doc's
// framing of LoadPackage as parsing driver package *source* files) -
// Uninstall's parameters are the already-resolved registry/tree locations
// and the service name directly.
//
// Uninstall takes driverStoreParent (the DriverStore FileRepository
// directory, or whatever directory actually contains the package's folder)
// plus driverStoreDirName, rather than the package's own *wim.DirEntry
// directly, because wim.DirEntry has no parent pointer: removing a node
// from the tree requires mutating its parent's Children slice, so the
// parent must be supplied. This is the same reason Uninstall takes no
// *wim.ImageMetadata parameter at all (unlike Install) - driverStoreParent
// (already navigated to by the caller) serves as the mutation point
// directly, with no need to walk down from an image root.
//
// driverStoreParent is mutated: driverStoreDirName's DirEntry (if present)
// is removed from driverStoreParent.Children, and every stream hash under
// its removed subtree that matches a bt entry has that entry's RefCount
// decremented by one (see decrementBlobRefs's doc comment for the exact,
// deliberately limited, refcount-adjustment policy). If driverStoreDirName
// is not (or is no longer) a child of driverStoreParent, this step is a
// no-op rather than an error: Uninstall is meant to be safely callable
// against a target that may have already been partially or fully cleaned
// up (by an earlier call, or by other means, e.g. a caller that only wants
// the registry side redone), and "the folder that should be removed is
// already gone" is success by that measure, not failure. bt may be nil (no
// refcount adjustment is attempted, e.g. if the caller does not have the
// image's blob table in hand) or non-nil.
//
// If currentControlSet is non-nil and has a "Services" subkey, serviceName's
// subkey is removed via service.Delete; service.Delete's ErrNotFound (no
// such Services\<serviceName> subkey) is deliberately swallowed rather than
// propagated, for the same "already gone is success" reasoning as the
// DriverStore-folder step above - this is a different judgment call than
// InstallRegistry's (and service.Delete's own) convention of erroring on an
// unexpected precondition: there, an inconsistent tree going into an
// *install* indicates a caller bug worth surfacing immediately; here, the
// entire point of Uninstall is to reach a "not present" end state, so
// discovering it is already reached is not an error. If currentControlSet
// has no "Services" subkey at all, this step is likewise a no-op. If
// currentControlSet is nil altogether, this step and the
// CriticalDeviceDatabase step below (both reached via currentControlSet)
// are skipped entirely, so a caller uninstalling a driver that has
// DriverStore files but no registry footprint (or that wants to handle the
// registry side separately) can pass nil.
//
// CriticalDeviceDatabase entries are matched by their own "Service" value
// (see criticaldevicedatabase.go's citations for why this is the documented
// association from a CDDB entry back to a service) equaling serviceName
// exactly, byte-for-byte after UTF-16LE decoding (case-sensitive, matching
// how mergeCriticalDeviceDatabase writes it verbatim from the resolved
// AddService name rather than case-folding it); entries with no "Service"
// value, or a different one, are left untouched - so uninstalling one
// driver never disturbs another driver's CriticalDeviceDatabase
// registration.
func Uninstall(bt *wim.BlobTable, currentControlSet *regf.Key, driverStoreParent *wim.DirEntry, driverStoreDirName string, serviceName string) error {
	if driverStoreParent == nil {
		return wrapErr("uninstall", errors.New("nil DriverStore parent directory"))
	}
	if driverStoreDirName == "" {
		return wrapErr("uninstall", errors.New("no DriverStore directory name given"))
	}
	if serviceName == "" {
		return wrapErr("uninstall", errors.New("no service name given"))
	}

	if removed := removeChild(driverStoreParent, driverStoreDirName); removed != nil {
		decrementBlobRefs(bt, removed)
	}

	if currentControlSet != nil {
		if servicesKey := service.FindSubkey(currentControlSet, "Services"); servicesKey != nil {
			if err := service.Delete(servicesKey, serviceName); err != nil && !errors.Is(err, service.ErrNotFound) {
				return wrapErr("uninstall", err)
			}
		}

		if controlKey := service.FindSubkey(currentControlSet, "Control"); controlKey != nil {
			if cddbKey := service.FindSubkey(controlKey, "CriticalDeviceDatabase"); cddbKey != nil {
				removeCriticalDeviceDatabaseEntries(cddbKey, serviceName)
			}
		}
	}

	return nil
}

// removeChild removes parent's direct child named name from
// parent.Children (matching Windows' case-insensitive namespace, mirroring
// install.go's findChildFold), returning the removed child (nil if no child
// matched) - the tree-mutation counterpart of findChildFold, used by
// Uninstall to detach a driver's DriverStore package folder.
func removeChild(parent *wim.DirEntry, name string) *wim.DirEntry {
	for i, c := range parent.Children {
		if strings.EqualFold(c.NameUTF8(), name) {
			parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
			return c
		}
	}
	return nil
}

// decrementBlobRefs walks removed and its full subtree, decrementing, in
// bt, the RefCount of every blob-table entry whose Hash matches one of
// removed's streams (including named alternate streams, and streams
// belonging to any nested children) - one decrement per stream, mirroring
// how Install increments RefCount once per payload file whose hash was
// already present in the table.
//
// It deliberately never removes a blob-table entry outright, even if its
// RefCount reaches zero: a WIM can hold multiple images, and a single blob
// can legitimately be referenced by dentries in other images (or elsewhere
// in this same image) that this function has no visibility into - Uninstall
// is only given the one removed subtree, not the whole WIM. Deciding
// whether a zero-RefCount entry is truly garbage (and only then reclaiming
// its space when the WIM is eventually rewritten) is a whole-WIM-aware
// concern that belongs to a higher-level caller, not something this
// function should guess at.
//
// It also never lets a RefCount underflow past 0: if bookkeeping has
// already drifted (e.g. a hash this subtree references was never actually
// counted by an Install call this package's caller made - such as a blob
// this package was never shown, already at RefCount 0 for some other
// reason), clamping at 0 is safer than wrapping around to a huge value.
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

// removeCriticalDeviceDatabaseEntries removes every direct subkey of
// cddbKey whose own "Service" value equals serviceName, leaving all others
// (i.e. other drivers' hardware-ID registrations) untouched.
func removeCriticalDeviceDatabaseEntries(cddbKey *regf.Key, serviceName string) {
	kept := cddbKey.Subkeys[:0]
	for _, k := range cddbKey.Subkeys {
		if cddbServiceValue(k) == serviceName {
			continue
		}
		kept = append(kept, k)
	}
	cddbKey.Subkeys = kept
}

// cddbServiceValue decodes a CriticalDeviceDatabase subkey's own "Service"
// value (see criticaldevicedatabase.go's citations) back to a Go string, or
// "" if absent - the read-side counterpart of mergeCriticalDeviceDatabase's
// write of the same value. Kept private to driver rather than added as new
// exported service package API, mirroring registryinstall.go's own existing
// local helpers.
func cddbServiceValue(key *regf.Key) string {
	v := service.FindValue(key, "Service")
	if v == nil {
		return ""
	}
	return utf16LEToString(v.Data)
}

// utf16LEToString decodes UTF-16LE bytes (no BOM, no terminator) back to a
// Go string - the reverse of stringToUTF16LE (install.go). wim's and
// service's own decode helpers of the same shape are unexported, so this is
// duplicated here, mirroring how install.go already duplicates the
// encode-side stringToUTF16LE for the same reason.
func utf16LEToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}
