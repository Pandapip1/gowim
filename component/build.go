package component

import (
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/mum"
	"github.com/Pandapip1/gowim/pa30"
	"github.com/Pandapip1/gowim/wim"
)

// PackagesDir and ManifestsDir are the standard offline-image directories
// BuildFromImage enumerates.
const (
	PackagesDir  = `Windows\servicing\Packages`
	ManifestsDir = `Windows\WinSxS\Manifests`
)

// ParseMUM builds an Entry for a package-level `.mum` file's raw bytes
// (plain XML; see the sibling `mum` package).
func ParseMUM(fileName string, data []byte) *Entry {
	e := &Entry{Kind: KindPackage, FileName: fileName}
	m, err := mum.Parse(data)
	if err != nil {
		e.Err = fmt.Errorf("component: parse %s: %w", fileName, err)
		return e
	}
	e.Manifest = m
	e.Identity = m.Identity
	e.Dependencies = dependenciesOf(m)
	return e
}

// ParseManifest builds an Entry for a component-level `.manifest` file's
// raw on-disk bytes (the "DCM"+version-byte-prefixed, PA30-compressed
// form), using dict as the shared source buffer PA30 decoding needs (see
// the sibling `pa30` package's DecodeWithSource and its testdata/
// wcp_dictionary.bin). A file needing SRC/FULLSRC support `pa30` doesn't
// implement yet, or any other decode/parse failure, is reported via the
// returned Entry's Err field rather than a second return value, so callers
// building a Store from many files can collect every entry uniformly (see
// package docs).
func ParseManifest(fileName string, data []byte, dict []byte) *Entry {
	e := &Entry{Kind: KindComponent, FileName: fileName}
	if len(data) < 4 || string(data[0:3]) != "DCM" {
		e.Err = fmt.Errorf("component: %s: missing DCM prefix", fileName)
		return e
	}
	xmlData, _, err := pa30.DecodeWithSource(data[4:], dict)
	if err != nil {
		e.Err = fmt.Errorf("component: decode %s: %w", fileName, err)
		return e
	}
	m, err := mum.Parse(xmlData)
	if err != nil {
		e.Err = fmt.Errorf("component: parse %s: %w", fileName, err)
		return e
	}
	e.Manifest = m
	e.Identity = m.Identity
	e.Dependencies = dependenciesOf(m)
	return e
}

// BuildFromImage enumerates PackagesDir's `*.mum` files and ManifestsDir's
// `*.manifest` files under root (an offline image's root directory tree, as
// read by the sibling `wim` package), parses each via ParseMUM/
// ParseManifest, and returns a Store over the results. Individual file
// read/decode/parse failures are recorded on that file's Entry.Err, not
// returned as a function error -- only a failure to enumerate the two
// top-level directories themselves (e.g. neither exists) is. dict is the
// shared PA30 source buffer passed to ParseManifest (see its doc comment).
func BuildFromImage(r *wim.Reader, root *wim.DirEntry, bt *wim.BlobTable, dict []byte) (*Store, error) {
	var entries []*Entry

	pkgDir, err := root.ReadDir(PackagesDir)
	if err != nil {
		return nil, fmt.Errorf("component: read %s: %w", PackagesDir, err)
	}
	for _, c := range pkgDir {
		if c.IsDirectory() || !strings.HasSuffix(strings.ToLower(c.NameUTF8()), ".mum") {
			continue
		}
		name := c.NameUTF8()
		data, err := r.ReadFile(root, bt, PackagesDir+`\`+name)
		if err != nil {
			entries = append(entries, &Entry{Kind: KindPackage, FileName: name, Err: fmt.Errorf("component: read %s: %w", name, err)})
			continue
		}
		entries = append(entries, ParseMUM(name, data))
	}

	manDir, err := root.ReadDir(ManifestsDir)
	if err != nil {
		return nil, fmt.Errorf("component: read %s: %w", ManifestsDir, err)
	}
	for _, c := range manDir {
		if c.IsDirectory() || !strings.HasSuffix(strings.ToLower(c.NameUTF8()), ".manifest") {
			continue
		}
		name := c.NameUTF8()
		data, err := r.ReadFile(root, bt, ManifestsDir+`\`+name)
		if err != nil {
			entries = append(entries, &Entry{Kind: KindComponent, FileName: name, Err: fmt.Errorf("component: read %s: %w", name, err)})
			continue
		}
		entries = append(entries, ParseManifest(name, data, dict))
	}

	return Build(entries), nil
}
