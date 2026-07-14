package component

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

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
// raw on-disk bytes, using dict as the shared source buffer PA30 decoding
// needs (see the sibling `pa30` package's DecodeWithSource and its
// testdata/wcp_dictionary.bin). Most real files are "DCM"+version-byte-
// prefixed, PA30-compressed XML, but some -- confirmed 2026-07-13 while
// measuring this package's decode coverage against a full real image's
// Manifests directory (17189 files): older, pre-CBS runtime component
// manifests such as the VC++ 8.0/9.0 CRT's, 193 of that sample -- are
// instead already-plain, uncompressed XML with no PA30 layer at all. Files
// missing the DCM prefix are parsed directly as XML rather than treated as
// an error; any other decode/parse failure is reported via the returned
// Entry's Err field rather than a second return value, so callers building
// a Store from many files can collect every entry uniformly (see package
// docs).
func ParseManifest(fileName string, data []byte, dict []byte) *Entry {
	e := &Entry{Kind: KindComponent, FileName: fileName}
	xmlData := data
	if len(data) >= 3 && string(data[0:3]) == "DCM" {
		var err error
		xmlData, _, err = pa30.DecodeWithSource(data[4:], dict)
		if err != nil {
			e.Err = fmt.Errorf("component: decode %s: %w", fileName, err)
			return e
		}
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
	var jobs []func() *Entry

	pkgDir, err := root.ReadDir(PackagesDir)
	if err != nil {
		return nil, fmt.Errorf("component: read %s: %w", PackagesDir, err)
	}
	for _, c := range pkgDir {
		if c.IsDirectory() || !strings.HasSuffix(strings.ToLower(c.NameUTF8()), ".mum") {
			continue
		}
		name := c.NameUTF8()
		jobs = append(jobs, func() *Entry {
			data, err := r.ReadFile(root, bt, PackagesDir+`\`+name)
			if err != nil {
				return &Entry{Kind: KindPackage, FileName: name, Err: fmt.Errorf("component: read %s: %w", name, err)}
			}
			return ParseMUM(name, data)
		})
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
		jobs = append(jobs, func() *Entry {
			data, err := r.ReadFile(root, bt, ManifestsDir+`\`+name)
			if err != nil {
				return &Entry{Kind: KindComponent, FileName: name, Err: fmt.Errorf("component: read %s: %w", name, err)}
			}
			return ParseManifest(name, data, dict)
		})
	}

	return Build(runJobsParallel(jobs)), nil
}

// runJobsParallel runs each of jobs (each independent: its own file
// read+parse, with no shared mutable state -- *wim.Reader's ReadFile goes
// through ReadAt, safe for concurrent use the same way os.File.ReadAt is)
// on a bounded worker pool (min(len(jobs), GOMAXPROCS)) and returns their
// results in any order -- order doesn't matter here since Build only
// indexes entries into a map afterward. See TODO.md's "Performance:
// concurrency opportunities" entry for why this loop was worth
// parallelizing (a real image's servicing\Packages + WinSxS\Manifests
// together number in the tens of thousands of files).
func runJobsParallel(jobs []func() *Entry) []*Entry {
	if len(jobs) == 0 {
		return nil
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}

	results := make([]*Entry, len(jobs))
	jobIdx := make(chan int, workers*2)
	go func() {
		defer close(jobIdx)
		for i := range jobs {
			jobIdx <- i
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobIdx {
				results[i] = jobs[i]()
			}
		}()
	}
	wg.Wait()
	return results
}
