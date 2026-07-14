package appx

import (
	"crypto/sha1"
	"errors"
	"testing"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/wim"
)

func TestFamilyNameFromFullName(t *testing.T) {
	tests := []struct {
		fullName string
		want     string
		wantErr  bool
	}{
		{"Microsoft.Paint_11.2201.22.0_x64__8wekyb3d8bbwe", "Microsoft.Paint_8wekyb3d8bbwe", false},
		{"Microsoft.Paint_2022.507.446.0_neutral_~_8wekyb3d8bbwe", "Microsoft.Paint_8wekyb3d8bbwe", false},
		{"not_enough_parts", "", true},
	}
	for _, tt := range tests {
		got, err := FamilyNameFromFullName(tt.fullName)
		if (err != nil) != tt.wantErr {
			t.Errorf("FamilyNameFromFullName(%q) err = %v, wantErr %v", tt.fullName, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("FamilyNameFromFullName(%q) = %q, want %q", tt.fullName, got, tt.want)
		}
	}
}

// TestRemoveProvisionedRealPaintFamily reproduces removing the real
// Microsoft.Paint family from the real AppxProvisioning.xml fixture: its
// bundle, main package, and per-scale resource packages (4 <Provisioned>
// entries total in the real file - see testdata/real_AppxProvisioning.xml)
// must all be removed together, and only those.
func TestRemoveProvisionedRealPaintFamily(t *testing.T) {
	pl, err := ParseProvisioning(realProvisioning)
	if err != nil {
		t.Fatalf("ParseProvisioning: %v", err)
	}
	before := len(pl.Provisioned)

	familyName := "Microsoft.Paint_8wekyb3d8bbwe"
	removed := RemoveProvisioned(pl, familyName, true)

	wantRemoved := []string{
		"Microsoft.Paint_2022.507.446.0_neutral_~_8wekyb3d8bbwe",
		"Microsoft.Paint_11.2201.22.0_x64__8wekyb3d8bbwe",
		"Microsoft.Paint_11.2201.22.0_neutral_split.scale-100_8wekyb3d8bbwe",
		"Microsoft.Paint_11.2201.22.0_neutral_split.scale-125_8wekyb3d8bbwe",
	}
	if len(removed) != len(wantRemoved) {
		t.Fatalf("removed %d entries, want %d: %v", len(removed), len(wantRemoved), removed)
	}
	for _, want := range wantRemoved {
		var found bool
		for _, got := range removed {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q among removed entries, got %v", want, removed)
		}
	}

	if len(pl.Provisioned) != before-len(wantRemoved) {
		t.Errorf("remaining Provisioned count = %d, want %d", len(pl.Provisioned), before-len(wantRemoved))
	}
	for _, p := range pl.Provisioned {
		if fam, err := FamilyNameFromFullName(p.FullName); err == nil && fam == familyName {
			t.Errorf("Provisioned still contains a %s entry: %q", familyName, p.FullName)
		}
	}

	var foundEOL bool
	for _, e := range pl.EndOfLife {
		if e.FamilyName == familyName {
			foundEOL = true
		}
	}
	if !foundEOL {
		t.Error("EndOfLife does not contain the removed family name")
	}
}

func TestRemoveProvisionedNoMatch(t *testing.T) {
	pl := &ProvisionList{Provisioned: []ProvisionedPackage{
		{FullName: "Contoso.App_1.0.0.0_x64__abcdefghijklm"},
	}}
	removed := RemoveProvisioned(pl, "NoSuch.Family_8wekyb3d8bbwe", false)
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none", removed)
	}
	if len(pl.Provisioned) != 1 {
		t.Errorf("Provisioned mutated when nothing matched: %v", pl.Provisioned)
	}
}

func TestRemoveFullPipeline(t *testing.T) {
	const fullName = "Contoso.App_1.0.0.0_x64__abcdefghijklm"
	const familyName = "Contoso.App_abcdefghijklm"

	pl := &ProvisionList{Provisioned: []ProvisionedPackage{{FullName: fullName}}}

	applications := &regf.Key{}
	applications.FindOrCreateSubkey(fullName)
	deprovisioned := &regf.Key{}

	root := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
	data := []byte("appxmanifest contents")
	hash := wim.Hash(sha1.Sum(data))
	if _, err := root.Add(`Program Files\WindowsApps\`+fullName+`\AppxManifest.xml`, hash); err != nil {
		t.Fatalf("root.Add: %v", err)
	}
	bt := &wim.BlobTable{Entries: []wim.BlobDescriptor{{Hash: hash, RefCount: 1}}}

	if err := Remove(pl, familyName, true, applications, deprovisioned, root, bt); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if len(pl.Provisioned) != 0 {
		t.Errorf("Provisioned = %v, want empty", pl.Provisioned)
	}
	if len(pl.EndOfLife) != 1 || pl.EndOfLife[0].FamilyName != familyName {
		t.Errorf("EndOfLife = %v, want [%s]", pl.EndOfLife, familyName)
	}
	if applications.Subkey(fullName) != nil {
		t.Error("Applications subkey still present after Remove")
	}
	if deprovisioned.Subkey(familyName) == nil {
		t.Error("Deprovisioned marker subkey not created")
	}
	if _, err := root.Lookup(`Program Files\WindowsApps\` + fullName); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf("WindowsApps folder still present after Remove, err = %v", err)
	}
	if bt.Entries[0].RefCount != 0 {
		t.Errorf("RefCount = %d, want 0 after removing the only reference", bt.Entries[0].RefCount)
	}
}

func TestRemoveAlreadyGoneIsSuccess(t *testing.T) {
	pl := &ProvisionList{}
	root := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
	applications := &regf.Key{}
	deprovisioned := &regf.Key{}

	if err := Remove(pl, "Contoso.App_abcdefghijklm", false, applications, deprovisioned, root, nil); err != nil {
		t.Fatalf("Remove of an already-absent package returned an error: %v", err)
	}
	if deprovisioned.Subkey("Contoso.App_abcdefghijklm") == nil {
		t.Error("Deprovisioned marker subkey not created even though the package had no Provisioned entry")
	}
}
