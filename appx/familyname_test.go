package appx

import "testing"

// TestPublisherIDReferenceValues reproduces the reference test vectors from
// github.com/russellbanks/package-family-name's src/publisher_id.rs and
// src/lib.rs (read 2026-07-14 from a local clone of the upstream repo at
// /tmp/package-family-name - see appx.go's doc comment), cross-validating
// this package's independent Go implementation against known-good output
// from a different-language reimplementation of the same (undocumented in
// prose) algorithm.
func TestPublisherIDReferenceValues(t *testing.T) {
	tests := []struct {
		publisher string
		want      string
	}{
		{"Publisher Software", "zj75k085cmj1a"},
		{"CN=Microsoft Corporation, O=Microsoft Corporation, L=Redmond, S=Washington, C=US", "8wekyb3d8bbwe"},
		{"CN=Hydraulic Software AG, O=Hydraulic Software AG, L=Zürich, S=Zürich, C=CH, SERIALNUMBER=CHE-312.597.948, OID.1.3.6.1.4.1.311.60.2.1.2=Zürich, OID.1.3.6.1.4.1.311.60.2.1.3=CH, OID.2.5.4.15=Private Organization", "fg3qp2cw01ypp"},
	}
	for _, tt := range tests {
		if got := PublisherID(tt.publisher); got != tt.want {
			t.Errorf("PublisherID(%q) = %q, want %q", tt.publisher, got, tt.want)
		}
	}
}

// TestPackageFamilyNameReal cross-checks PackageFamilyName against the real
// StickyNotes AppxManifest.xml's Identity: the real WindowsApps folder this
// manifest was extracted from is named
// "Microsoft.MicrosoftStickyNotes_4.0.6104.0_x64__8wekyb3d8bbwe" (see
// manifest_test.go), whose trailing "_8wekyb3d8bbwe" is the same
// PublisherID this package independently derives from the manifest's own
// Identity.Publisher string.
func TestPackageFamilyNameReal(t *testing.T) {
	m, err := ParseManifest(realStickyNotesManifest)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	want := "Microsoft.MicrosoftStickyNotes_8wekyb3d8bbwe"
	if got := PackageFamilyName(m.Identity.Name, m.Identity.Publisher); got != want {
		t.Errorf("PackageFamilyName = %q, want %q", got, want)
	}
}
