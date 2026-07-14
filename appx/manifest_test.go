package appx

import (
	_ "embed"
	"testing"
)

// realStickyNotesManifest is a real Windows 11 23H2 AppxManifest.xml,
// copied verbatim (2026-07-14) from
// Program Files\WindowsApps\Microsoft.MicrosoftStickyNotes_4.0.6104.0_x64__8wekyb3d8bbwe\AppxManifest.xml
// via a read-only guestmount of a real un-booted VM image - see appx.go's
// doc comment. The containing folder's name is itself the package's
// PackageFullName, providing an independent cross-check of both Identity
// parsing and PackageFamilyName derivation (see TestPackageFamilyNameReal).
//
//go:embed testdata/real_StickyNotes_AppxManifest.xml
var realStickyNotesManifest []byte

func TestParseManifestReal(t *testing.T) {
	m, err := ParseManifest(realStickyNotesManifest)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	want := Identity{
		Name:                  "Microsoft.MicrosoftStickyNotes",
		Publisher:             "CN=Microsoft Corporation, O=Microsoft Corporation, L=Redmond, S=Washington, C=US",
		Version:               "4.0.6104.0",
		ProcessorArchitecture: "x64",
	}
	if m.Identity != want {
		t.Errorf("Identity = %+v, want %+v", m.Identity, want)
	}
}
