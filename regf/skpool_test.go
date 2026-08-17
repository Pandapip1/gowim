package regf

import (
	"bytes"
	"testing"
)

// TestAppendToDeduplicatesSecurityDescriptors guards against a real bug
// found 2026-07-15 via a real Windows 11 SOFTWARE hive (76.5MB): before
// skPool existed, every key with a non-nil Security got its own brand-new
// sk cell, even when byte-identical to another key's. A real hive's tens
// of thousands of keys typically share only a handful of unique
// descriptors, so this more than doubled the hive's size (76.5MB -> 181MB)
// and -- far worse than the size alone -- produced a hive that still
// parsed back correctly (fooling this package's own round-trip tests, all
// of which used small synthetic hives) but made Windows hang indefinitely
// during first-logon specialize on the image it was embedded in.
//
// This test builds a small hive with three keys sharing one Security
// descriptor and a fourth with a different one, confirming: (1) only two
// "sk" cells actually appear in the serialized output (not four), (2) the
// shared cell's stored refCount is 3, and (3) every key's Security still
// round-trips correctly through Parse despite the sharing.
func TestAppendToDeduplicatesSecurityDescriptors(t *testing.T) {
	sharedSD := []byte{0x01, 0x00, 0x04, 0x80, 0xAA, 0xBB, 0xCC, 0xDD}
	otherSD := []byte{0x01, 0x00, 0x04, 0x80, 0x11, 0x22, 0x33, 0x44}

	hive := &Hive{
		BaseBlock: BaseBlock{
			MajorVersion:     1,
			MinorVersion:     Version1_5,
			FileType:         FileTypePrimary,
			ClusteringFactor: 1,
		},
		Root: &Key{
			Flags:    KeyFlagHiveEntry,
			Security: sharedSD,
			Subkeys: []*Key{
				{Name: []byte("A"), Flags: KeyFlagCompName, Security: sharedSD},
				{Name: []byte("B"), Flags: KeyFlagCompName, Security: sharedSD},
				{Name: []byte("C"), Flags: KeyFlagCompName, Security: otherSD},
			},
		},
	}

	data, err := hive.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	skCount := bytes.Count(data, []byte("sk"))
	if skCount != 2 {
		t.Errorf("found %d \"sk\" cell signatures in output, want 2 (one shared by root/A/B, one for C)", skCount)
	}

	// Locate the shared cell's refCount directly: it's the sk cell whose
	// descriptor matches sharedSD.
	idx := bytes.Index(data, sharedSD)
	if idx < 0 {
		t.Fatal("shared descriptor bytes not found in output at all")
	}
	refCountOff := idx - skHeaderSize + 12 // descriptor immediately follows the header; refCount is header field @12
	if refCountOff < 0 || refCountOff+4 > len(data) {
		t.Fatal("computed refCount offset out of bounds")
	}
	refCount := le.Uint32(data[refCountOff : refCountOff+4])
	if refCount != 3 {
		t.Errorf("shared sk cell refCount = %d, want 3 (root, A, B)", refCount)
	}

	// Round-trip: every key's Security must still decode correctly despite
	// the sharing.
	reparsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !bytes.Equal(reparsed.Root.Security, sharedSD) {
		t.Errorf("root Security = % x, want % x", reparsed.Root.Security, sharedSD)
	}
	wantByName := map[string][]byte{"A": sharedSD, "B": sharedSD, "C": otherSD}
	for _, sub := range reparsed.Root.Subkeys {
		want := wantByName[sub.NameUTF8()]
		if !bytes.Equal(sub.Security, want) {
			t.Errorf("subkey %s Security = % x, want % x", sub.NameUTF8(), sub.Security, want)
		}
	}
}
