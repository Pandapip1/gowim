package wim

import (
	"fmt"
	"sync"
	"testing"
)

// hashOf returns a deterministic, distinct Hash for each i.
func hashOf(i int) Hash {
	var h Hash
	h[0] = byte(i)
	h[1] = byte(i >> 8)
	h[2] = byte(i >> 16)
	h[19] = 0xAA // marker so an all-zero Hash (i==0's low bytes) is never mistaken for the zero hash
	return h
}

// TestByHashDuplicateHashesReturnsLowestIndex confirms ByHash's documented
// "first matching entry in table order" behavior is preserved by the index:
// when several entries share a hash, the lowest-index one must win.
func TestByHashDuplicateHashesReturnsLowestIndex(t *testing.T) {
	dup := hashOf(42)
	bt := &BlobTable{Entries: []BlobDescriptor{
		{Hash: hashOf(1), RefCount: 1},
		{Hash: dup, RefCount: 100}, // index 1: first occurrence of dup
		{Hash: hashOf(2), RefCount: 2},
		{Hash: dup, RefCount: 200}, // index 3: later, duplicate occurrence
		{Hash: dup, RefCount: 300}, // index 4: later still
	}}

	got, ok := bt.ByHash(dup)
	if !ok {
		t.Fatalf("ByHash(dup): not found")
	}
	if got.RefCount != 100 {
		t.Errorf("ByHash(dup) = RefCount %d, want 100 (the first/lowest-index occurrence)", got.RefCount)
	}

	// Call again to make sure repeated lookups (post-index-build) are stable.
	got2, ok2 := bt.ByHash(dup)
	if !ok2 || got2.RefCount != 100 {
		t.Errorf("second ByHash(dup) = (%+v, %v), want RefCount 100, true", got2, ok2)
	}
}

// TestByHashIndexInvalidatedOnAppend confirms that entries appended to
// Entries *after* ByHash has already been called (and has therefore built
// its cached index) are still found, and that a duplicate hash introduced
// later than an existing one does not override the earlier, lower-index
// match.
func TestByHashIndexInvalidatedOnAppend(t *testing.T) {
	bt := &BlobTable{Entries: []BlobDescriptor{
		{Hash: hashOf(1), RefCount: 1},
		{Hash: hashOf(2), RefCount: 2},
	}}

	// Prime the index before mutation.
	if _, ok := bt.ByHash(hashOf(1)); !ok {
		t.Fatalf("ByHash(hashOf(1)) before append: not found")
	}
	if _, ok := bt.ByHash(hashOf(999)); ok {
		t.Fatalf("ByHash(hashOf(999)) before append: unexpectedly found")
	}

	// Mutate: append a new, distinct entry and a duplicate of an existing
	// hash.
	bt.Entries = append(bt.Entries,
		BlobDescriptor{Hash: hashOf(3), RefCount: 3},
		BlobDescriptor{Hash: hashOf(1), RefCount: 111}, // duplicate of index 0
	)

	// The newly appended, previously-unindexed hash must now be found.
	got3, ok3 := bt.ByHash(hashOf(3))
	if !ok3 || got3.RefCount != 3 {
		t.Errorf("ByHash(hashOf(3)) after append = (%+v, %v), want RefCount 3, true", got3, ok3)
	}

	// The pre-existing hash must still resolve to its original, lower-index
	// entry, not the newly appended duplicate.
	got1, ok1 := bt.ByHash(hashOf(1))
	if !ok1 || got1.RefCount != 1 {
		t.Errorf("ByHash(hashOf(1)) after append = (%+v, %v), want RefCount 1 (original, lowest index), true", got1, ok1)
	}
}

// TestByHashConsistencyAtScale builds a synthetic table with hundreds of
// entries, including deliberate duplicate hashes, and cross-checks every
// ByHash result against a naive linear scan (byHashLinearScan) both before
// and after further mutation, to catch any behavioral drift the index could
// introduce at realistic scale.
func TestByHashConsistencyAtScale(t *testing.T) {
	const n = 2000
	bt := &BlobTable{Entries: make([]BlobDescriptor, 0, n)}
	for i := 0; i < n; i++ {
		h := hashOf(i)
		if i%7 == 0 && i > 0 {
			// Introduce a duplicate of an earlier hash every 7th entry.
			h = hashOf(i / 2)
		}
		bt.Entries = append(bt.Entries, BlobDescriptor{Hash: h, RefCount: uint32(i)})
	}

	checkAll := func(label string) {
		t.Helper()
		for i := 0; i < n; i++ {
			h := hashOf(i)
			want, wantOK := bt.byHashLinearScan(h)
			got, gotOK := bt.ByHash(h)
			if gotOK != wantOK || got != want {
				t.Fatalf("%s: ByHash(%v) = (%+v, %v), want (%+v, %v)", label, h, got, gotOK, want, wantOK)
			}
		}
		// A hash guaranteed absent from the table.
		absent := hashOf(n + 100000)
		if _, ok := bt.ByHash(absent); ok {
			t.Fatalf("%s: ByHash(absent hash) unexpectedly found", label)
		}
	}

	checkAll("before mutation")

	// Mutate: append more entries, including further duplicates, after
	// ByHash has already built and used its index above.
	for i := n; i < n+500; i++ {
		h := hashOf(i)
		if i%5 == 0 {
			h = hashOf(i - n) // duplicate of an original entry
		}
		bt.Entries = append(bt.Entries, BlobDescriptor{Hash: h, RefCount: uint32(i)})
	}

	for i := n; i < n+500; i++ {
		h := hashOf(i)
		want, wantOK := bt.byHashLinearScan(h)
		got, gotOK := bt.ByHash(h)
		if gotOK != wantOK || got != want {
			t.Fatalf("after growth, new range: ByHash(%v) = (%+v, %v), want (%+v, %v)", h, got, gotOK, want, wantOK)
		}
	}
	checkAll("after mutation, original range")
}

// TestByHashConcurrentFirstUse exercises ByHash from many goroutines on a
// single, not-yet-indexed BlobTable concurrently -- mirroring
// encodeBlobsPipeline's workers all calling BlobSource.Blob (which calls
// ByHash) on the same source table at once -- to catch data races in the
// lazy index build. Run with `go test -race` to be meaningful.
func TestByHashConcurrentFirstUse(t *testing.T) {
	const n = 5000
	bt := &BlobTable{Entries: make([]BlobDescriptor, n)}
	for i := 0; i < n; i++ {
		bt.Entries[i] = BlobDescriptor{Hash: hashOf(i), RefCount: uint32(i)}
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, ok := bt.ByHash(hashOf(i))
			if !ok {
				errs <- fmt.Errorf("hash %d: not found", i)
				return
			}
			if d.RefCount != uint32(i) {
				errs <- fmt.Errorf("hash %d: got RefCount %d, want %d", i, d.RefCount, i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
