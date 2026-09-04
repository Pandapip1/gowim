package wim

import (
	"fmt"
	"strings"
	"testing"
)

// childLinearScan is a scaffolding copy of Child's pre-index implementation
// (see path.go's original, before the childIndex cache was added), kept here
// only for the A/B benchmark comparison below. Delete this file's reliance on
// it (or the whole file) once the before/after numbers have been captured;
// it duplicates Child on purpose so the "before" arm exercises exactly the
// old code path rather than an approximation of it.
func childLinearScan(d *DirEntry, name string) *DirEntry {
	for _, c := range d.Children {
		if strings.EqualFold(c.NameUTF8(), name) {
			return c
		}
	}
	return nil
}

// buildBenchDir builds a synthetic directory with n children named the way
// real Windows\WinSxS\Manifests entries are (long, mostly-unique component
// manifest names), matching the ~28,069-entry scale the performance audit
// measured against a real image.
func buildBenchDir(n int) (*DirEntry, []string) {
	dir := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone}
	names := make([]string, n)
	dir.Children = make([]*DirEntry, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("amd64_component.name.%05d_31bf3856ad364e35_10.0.22621.6120_none_%08x.manifest", i, i)
		names[i] = name
		dir.Children[i] = &DirEntry{
			Name:    stringToUTF16LE(name),
			Streams: []Stream{{Hash: Hash{byte(i), byte(i >> 8)}}},
		}
	}
	return dir, names
}

// BenchmarkDirEntryChildLinearScan measures the old O(n), allocate-per-
// comparison implementation, for direct comparison against
// BenchmarkDirEntryChildIndexed below. Both look up the same 28,069-entry
// directory, in the same access pattern (every name once, worst case last
// since a linear scan's cost is dominated by misses/late matches).
func BenchmarkDirEntryChildLinearScan(b *testing.B) {
	dir, names := buildBenchDir(28069)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := names[i%len(names)]
		if got := childLinearScan(dir, name); got == nil {
			b.Fatalf("Child(%q) = nil", name)
		}
	}
}

// BenchmarkDirEntryChildIndexed measures the new cached-index Child
// implementation under the same conditions as BenchmarkDirEntryChildLinearScan.
func BenchmarkDirEntryChildIndexed(b *testing.B) {
	dir, names := buildBenchDir(28069)
	// Warm the index once, matching real usage where a directory is looked
	// into repeatedly after its first Child call.
	dir.Child(names[0])
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := names[i%len(names)]
		if got := dir.Child(name); got == nil {
			b.Fatalf("Child(%q) = nil", name)
		}
	}
}

// BenchmarkDirEntryChildIndexedColdEachTime measures Child's cost when the
// index must be rebuilt on every call (Children reassigned between lookups,
// as happens once per Add/Remove/Rename) - i.e. the worst case for the
// indexed approach, still expected to roughly track the old linear scan's
// single-pass cost since both do one O(n) pass, but the indexed version adds
// a map-build on top.
func BenchmarkDirEntryChildIndexedColdEachTime(b *testing.B) {
	dir, names := buildBenchDir(28069)
	children := dir.Children
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Force a fresh slice value each time so sameChildrenSlice reports a
		// miss and buildChildIndex runs again, as it would right after a
		// real mutation.
		dir.Children = append(children[:0:0], children...)
		name := names[i%len(names)]
		if got := dir.Child(name); got == nil {
			b.Fatalf("Child(%q) = nil", name)
		}
	}
}
