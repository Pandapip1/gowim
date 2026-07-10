package driver

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf16"

	"github.com/Pandapip1/gowim/pe"
	"github.com/Pandapip1/gowim/wim"
)

// NewBlob is one payload file's content that was not already present in the
// blob table, paired with the hash the caller should use to place it (e.g.
// when finally assembling an output WIM file - a future addition to the wim
// package, out of scope here).
type NewBlob struct {
	Hash wim.Hash
	Data []byte
}

// Install merges pkg's payload files into md's directory-entry tree and
// extends bt with any blob whose hash is not already present, deduplicating
// by hash (an existing entry's RefCount is incremented rather than a
// duplicate blob being added).
//
// destDirs maps each DIRID actually used by pkg's payload files (see
// PayloadFile.DirID) to the image-relative directory path that DIRID should
// resolve to, e.g. destDirs[driver.DirIDDriverStore] =
// `Windows\System32\DriverStore\FileRepository\mydriver.inf_amd64_xxxxxxxx`.
// Path strings may use '/' or '\' as the separator; both are accepted and
// normalized to path components. Install does not compute this path itself
// for DIRID 13 (see the package doc's stated non-goal regarding the
// DriverStore hashing scheme) - the caller must supply it (and any other
// DIRID the package's files use) explicitly. It is an error for a payload
// file's DirID to be absent from destDirs.
//
// Each payload file whose DestName ends in ".sys" (case-insensitively) is
// parsed with pe.Parse as a sanity check that it is a well-formed PE image
// before being installed; a parse error is returned rather than installing
// the file.
//
// It returns the (mutated) image metadata's root directory entry and the
// list of genuinely new blobs the caller must place in the eventual output
// WIM file.
func Install(md *wim.ImageMetadata, bt *wim.BlobTable, pkg *Package, destDirs map[DirID]string) (*wim.DirEntry, []NewBlob, error) {
	if md == nil {
		return nil, nil, wrapErr("install", errors.New("nil image metadata"))
	}
	if md.Root == nil {
		md.Root = &wim.DirEntry{
			Attributes: wim.FileAttributeDirectory,
			SecurityID: wim.SecurityIDNone,
		}
	}
	if bt == nil {
		return nil, nil, wrapErr("install", errors.New("nil blob table"))
	}

	existing := make(map[wim.Hash]*wim.BlobDescriptor, len(bt.Entries))
	for i := range bt.Entries {
		existing[bt.Entries[i].Hash] = &bt.Entries[i]
	}

	var newBlobs []NewBlob

	for _, pf := range pkg.Files {
		base, ok := destDirs[pf.DirID]
		if !ok {
			return nil, nil, wrapErr("install", fmt.Errorf(
				"no destination directory supplied for DIRID %d (file %s)", pf.DirID, pf.DestName))
		}

		data, err := fs.ReadFile(pkg.FSys, pf.SourcePath)
		if err != nil {
			return nil, nil, wrapErr("read payload file "+pf.SourcePath, err)
		}

		if strings.EqualFold(pathExt(pf.DestName), ".sys") {
			if _, err := pe.Parse(data); err != nil {
				return nil, nil, wrapErr("payload file "+pf.DestName+" is not a well-formed PE image", err)
			}
		}

		hash := wim.Hash(sha1.Sum(data))

		if desc, ok := existing[hash]; ok {
			desc.RefCount++
		} else {
			desc := wim.BlobDescriptor{
				Hash:       hash,
				RefCount:   1,
				PartNumber: 1,
				Resource: wim.ResourceHeader{
					UncompressedSize: uint64(len(data)),
				},
			}
			bt.Entries = append(bt.Entries, desc)
			existing[hash] = &bt.Entries[len(bt.Entries)-1]
			newBlobs = append(newBlobs, NewBlob{Hash: hash, Data: data})
		}

		components := pathComponents(base, pf.DirSubdir, pf.DestName)
		if len(components) == 0 {
			return nil, nil, wrapErr("install", fmt.Errorf("empty destination path for file %s", pf.DestName))
		}
		if err := placeFile(md.Root, components, hash); err != nil {
			return nil, nil, err
		}
	}

	return md.Root, newBlobs, nil
}

// pathComponents splits base, subdir, and name (each possibly '/'- or
// '\'-separated) into a single flat list of non-empty path components.
func pathComponents(parts ...string) []string {
	var out []string
	for _, p := range parts {
		p = toSlash(p)
		for _, c := range strings.Split(p, "/") {
			if c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// pathExt returns the extension of a file name, including the leading dot,
// or "" if it has none.
func pathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// placeFile walks/creates directory entries for components[:len-1] under
// root, then creates or overwrites the leaf entry components[len-1] as a
// regular file whose unnamed stream has the given hash.
func placeFile(root *wim.DirEntry, components []string, hash wim.Hash) error {
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
			return wrapErr("install", fmt.Errorf("path component %q already exists as a non-directory", name))
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
// terminator), matching the encoding wim.DirEntry.Name/ShortName expect.
// wim's own helper of the same shape is unexported, so it is duplicated here
// (mirroring how cat/der.go duplicates it from wim/encoding.go).
func stringToUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		out[i*2] = byte(c)
		out[i*2+1] = byte(c >> 8)
	}
	return out
}
