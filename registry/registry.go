// Package registry ties the sibling regf package (registry hive file
// format) to the sibling wim package (offline image file access), so a
// caller can load, mutate, and save back the standard set of registry
// hives inside an offline Windows image without hand-rolling the path
// lookups and blob-table bookkeeping itself -- this is the "registry
// generalization" work referenced in the top-level TODO.md.
//
// It does not itself parse the regf format or the WIM format (see regf and
// wim for that), and it does not write the final output WIM file: like the
// sibling driver package's Install/Uninstall, LoadHiveSet/Hive.Save only
// produce/mutate in-memory structures (a *regf.Hive tree, a *wim.DirEntry's
// stream hash, a *wim.BlobTable's entries) plus any newly-produced blob
// bytes the caller must place when it eventually assembles/writes the WIM
// file -- that step is out of scope for any package below the top-level
// orchestration tool.
package registry

import (
	"crypto/sha1"
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/wim"
)

// Standard hive names (see standardHivePaths for the image-relative path
// each one conventionally lives at), matching TODO.md's "standard hive set"
// list.
const (
	HiveSystem      = "SYSTEM"
	HiveSoftware    = "SOFTWARE"
	HiveDefault     = "DEFAULT"
	HiveSAM         = "SAM"
	HiveComponents  = "COMPONENTS"
	HiveDefaultUser = "NTUSER.DAT" // Users\Default\NTUSER.DAT
)

// standardHivePaths maps each standard hive name (see the Hive* constants)
// to its conventional image-relative path.
var standardHivePaths = map[string]string{
	HiveSystem:      `Windows\System32\config\SYSTEM`,
	HiveSoftware:    `Windows\System32\config\SOFTWARE`,
	HiveDefault:     `Windows\System32\config\DEFAULT`,
	HiveSAM:         `Windows\System32\config\SAM`,
	HiveComponents:  `Windows\System32\config\COMPONENTS`,
	HiveDefaultUser: `Users\Default\NTUSER.DAT`,
}

// Hive is one loaded registry hive from an offline image: its parsed tree,
// plus the WIM location its bytes came from so Save knows where to write
// modified bytes back.
type Hive struct {
	// Name is one of the Hive* constants.
	Name string
	// Path is the image-relative path Name was loaded from (see
	// standardHivePaths).
	Path string
	// Entry is the WIM directory entry for Path -- mutated in place by Save
	// to point at the re-serialized hive's new content hash.
	Entry *wim.DirEntry
	// Hive is the parsed (and, presumably, caller-modified) hive tree.
	Hive *regf.Hive
}

// HiveSet is the standard set of registry hives found in an offline Windows
// image (see the Hive* constants), keyed by name.
type HiveSet struct {
	Hives map[string]*Hive
}

// NewBlob is a hive's newly-serialized content that was not already present
// in the blob table, paired with the hash the caller should use to place it
// when finally assembling an output WIM file. Mirrors the sibling driver
// package's own NewBlob type -- duplicated rather than imported, matching
// this project's convention of small sibling-package type duplication over
// a cross-cutting dependency (see driver/install.go's own such note).
type NewBlob struct {
	Hash wim.Hash
	Data []byte
}

// LoadHiveSet locates and parses every standard hive present under root
// (see standardHivePaths). A hive that does not exist in this image (e.g. a
// WinPE image with no SAM/COMPONENTS hive) is simply omitted from the
// result rather than causing an error; only a hive that exists but fails to
// read or parse is an error.
func LoadHiveSet(r *wim.Reader, root *wim.DirEntry, bt *wim.BlobTable) (*HiveSet, error) {
	if root == nil {
		return nil, fmt.Errorf("registry: load hive set: nil root directory entry")
	}
	if bt == nil {
		return nil, fmt.Errorf("registry: load hive set: nil blob table")
	}

	hs := &HiveSet{Hives: map[string]*Hive{}}
	for name, path := range standardHivePaths {
		entry, err := root.Lookup(path)
		if err != nil {
			if errors.Is(err, wim.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("registry: load hive set: %s: %w", name, err)
		}

		data, err := r.ReadFile(root, bt, path)
		if err != nil {
			return nil, fmt.Errorf("registry: load hive set: %s: %w", name, err)
		}
		h, err := regf.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("registry: load hive set: %s: parse: %w", name, err)
		}

		hs.Hives[name] = &Hive{Name: name, Path: path, Entry: entry, Hive: h}
	}
	return hs, nil
}

// Save re-serializes h.Hive (presumably modified by the caller) via
// regf.Hive.AppendTo, points h.Entry's unnamed stream at the result's
// hash, and updates bt to match: deduplicating by hash and incrementing an
// existing entry's RefCount exactly like the sibling driver package's
// Install, and decrementing the hive's previous hash's RefCount exactly
// like its Uninstall (see those functions' doc comments for the same
// never-let-RefCount-underflow, never-reclaim-here reasoning -- deciding
// whether a zero-RefCount entry is genuinely garbage is a whole-WIM-aware
// concern for a higher-level caller, not this function).
//
// It returns the new blob's data if genuinely new (a zero NewBlob if the
// re-serialized bytes happen to already match a blob already in bt), which
// the caller must place in the eventual output WIM file.
func (h *Hive) Save(bt *wim.BlobTable) (NewBlob, error) {
	if h.Hive == nil {
		return NewBlob{}, fmt.Errorf("registry: save hive %s: nil Hive", h.Name)
	}
	if h.Entry == nil {
		return NewBlob{}, fmt.Errorf("registry: save hive %s: nil Entry", h.Name)
	}
	if bt == nil {
		return NewBlob{}, fmt.Errorf("registry: save hive %s: nil blob table", h.Name)
	}

	data, err := h.Hive.AppendTo(nil)
	if err != nil {
		return NewBlob{}, fmt.Errorf("registry: save hive %s: %w", h.Name, err)
	}
	hash := wim.Hash(sha1.Sum(data))
	oldHash := h.Entry.MainHash()

	if oldHash != hash {
		decrementRefCount(bt, oldHash)
	}

	var newBlob NewBlob
	if desc, ok := findBlobDescriptor(bt, hash); ok {
		if oldHash != hash {
			desc.RefCount++
		}
	} else {
		bt.Entries = append(bt.Entries, wim.BlobDescriptor{
			Hash:       hash,
			RefCount:   1,
			PartNumber: 1,
			Resource: wim.ResourceHeader{
				UncompressedSize: uint64(len(data)),
			},
		})
		newBlob = NewBlob{Hash: hash, Data: data}
	}

	h.Entry.Streams = []wim.Stream{{Hash: hash}}
	return newBlob, nil
}

// findBlobDescriptor returns a pointer to bt's entry for hash, if any.
func findBlobDescriptor(bt *wim.BlobTable, hash wim.Hash) (*wim.BlobDescriptor, bool) {
	for i := range bt.Entries {
		if bt.Entries[i].Hash == hash {
			return &bt.Entries[i], true
		}
	}
	return nil, false
}

// decrementRefCount decrements hash's blob-table entry's RefCount by one,
// never letting it underflow past 0, and does nothing if hash is zero (the
// format's "empty stream" convention, which has no blob-table entry) or has
// no matching entry at all.
func decrementRefCount(bt *wim.BlobTable, hash wim.Hash) {
	if hash.IsZero() {
		return
	}
	if desc, ok := findBlobDescriptor(bt, hash); ok && desc.RefCount > 0 {
		desc.RefCount--
	}
}
