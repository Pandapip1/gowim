package wim

import "fmt"

// ReadFile reads the fully-resolved, uncompressed contents of the file at
// path, resolved from root (see DirEntry.Lookup for path syntax), using bt to
// map the file's unnamed stream's hash to its blob-table entry and thus its
// resource location.
//
// If the resolved blob is stored compressed, ReadFile returns
// ErrCompressedResource unmodified (not wrapped), exactly as
// Reader.resourceData does for any other resource read via this package: this
// package handles WIM container structure, not the LZX/XPRESS/LZMS codecs (see
// the wim package doc and ErrCompressedResource).
//
// ReadFile returns an error wrapping ErrNotFound if path does not resolve, and
// a plain error if path resolves to a directory or its hash has no matching
// blob-table entry.
func (r *Reader) ReadFile(root *DirEntry, bt *BlobTable, path string) ([]byte, error) {
	entry, err := root.Lookup(path)
	if err != nil {
		return nil, err
	}
	if entry.IsDirectory() {
		return nil, fmt.Errorf("wim: read file %q: is a directory", path)
	}
	hash := entry.MainHash()
	if hash.IsZero() {
		// The all-zero hash is the format's convention for an empty stream;
		// there is no blob-table entry to look up.
		return nil, nil
	}
	desc, ok := bt.ByHash(hash)
	if !ok {
		return nil, fmt.Errorf("wim: read file %q: no blob-table entry for hash %s", path, hash)
	}
	// resourceData returns ErrCompressedResource for a compressed blob;
	// propagate it unmodified so callers can errors.Is against it.
	return r.resourceData(desc.Resource)
}
