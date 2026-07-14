package appx

import (
	_ "embed"
	"testing"
)

// realProvisioning is a real Windows 11 23H2 AppxProvisioning.xml, copied
// verbatim (2026-07-14) from ProgramData\Microsoft\Windows\
// AppxProvisioning.xml via a read-only guestmount of a real un-booted VM
// image - see appx.go's doc comment.
//
//go:embed testdata/real_AppxProvisioning.xml
var realProvisioning []byte

func TestParseProvisioningReal(t *testing.T) {
	pl, err := ParseProvisioning(realProvisioning)
	if err != nil {
		t.Fatalf("ParseProvisioning: %v", err)
	}

	if len(pl.EndOfLife) == 0 {
		t.Fatal("expected at least one EndOfLife entry")
	}
	if pl.EndOfLife[0].FamilyName != "Microsoft.Camera_8wekyb3d8bbwe" {
		t.Errorf("EndOfLife[0].FamilyName = %q, want %q", pl.EndOfLife[0].FamilyName, "Microsoft.Camera_8wekyb3d8bbwe")
	}

	if len(pl.Provisioned) == 0 {
		t.Fatal("expected at least one Provisioned entry")
	}
	var found bool
	for _, p := range pl.Provisioned {
		if p.FullName == "Microsoft.WindowsCalculator_2022.507.446.0_neutral_~_8wekyb3d8bbwe" {
			found = true
			if p.PackageType != "bundle" {
				t.Errorf("PackageType = %q, want %q", p.PackageType, "bundle")
			}
		}
		if p.FullName == "Microsoft.NET.Native.Framework.1.3_1.3.24211.0_x64__8wekyb3d8bbwe" && !p.IsLOBApp {
			t.Errorf("expected IsLOBApp=true for %q", p.FullName)
		}
	}
	if !found {
		t.Fatal("expected to find Microsoft.WindowsCalculator bundle entry")
	}
}

func TestProvisioningSerializeRoundTrip(t *testing.T) {
	pl, err := ParseProvisioning(realProvisioning)
	if err != nil {
		t.Fatalf("ParseProvisioning: %v", err)
	}

	data, err := pl.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	pl2, err := ParseProvisioning(data)
	if err != nil {
		t.Fatalf("ParseProvisioning(serialized): %v", err)
	}

	if len(pl2.Provisioned) != len(pl.Provisioned) {
		t.Errorf("round-tripped Provisioned count = %d, want %d", len(pl2.Provisioned), len(pl.Provisioned))
	}
	if len(pl2.EndOfLife) != len(pl.EndOfLife) {
		t.Errorf("round-tripped EndOfLife count = %d, want %d", len(pl2.EndOfLife), len(pl.EndOfLife))
	}
}
