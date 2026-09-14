package wufetch

import "testing"

func TestFindCapabilityFile_KnownCapability(t *testing.T) {
	files := map[string]File{
		"OpenSSH-Server-Package-amd64.cab": {Name: "OpenSSH-Server-Package-amd64.cab", SHA256: "aaa"},
		"OpenSSH-Client-Package-amd64.cab": {Name: "OpenSSH-Client-Package-amd64.cab", SHA256: "bbb"},
		"DesktopDeployment.cab":            {Name: "DesktopDeployment.cab"},
	}

	f, err := FindCapabilityFile(files, "OpenSSH.Server~~~~0.0.1.0", "amd64", "")
	if err != nil {
		t.Fatalf("FindCapabilityFile: %v", err)
	}
	if f.Name != "OpenSSH-Server-Package-amd64.cab" {
		t.Fatalf("got %q, want OpenSSH-Server-Package-amd64.cab", f.Name)
	}
}

func TestFindCapabilityFile_ArchMismatch(t *testing.T) {
	files := map[string]File{
		"OpenSSH-Server-Package-arm64.cab": {Name: "OpenSSH-Server-Package-arm64.cab"},
	}
	if _, err := FindCapabilityFile(files, "OpenSSH.Server~~~~0.0.1.0", "amd64", ""); err == nil {
		t.Fatal("expected error when only a different architecture's file is present")
	}
}

func TestFindCapabilityFile_UnknownCapabilityNeedsOverride(t *testing.T) {
	files := map[string]File{
		"Foo-Bar-Package-amd64.cab": {Name: "Foo-Bar-Package-amd64.cab"},
	}

	if _, err := FindCapabilityFile(files, "Foo.Bar~~~~0.0.1.0", "amd64", ""); err == nil {
		t.Fatal("expected ErrCapabilityUnknown for a capability not in KnownCapabilityPackages")
	} else if _, ok := err.(*ErrCapabilityUnknown); !ok {
		t.Fatalf("got %T, want *ErrCapabilityUnknown", err)
	}

	f, err := FindCapabilityFile(files, "Foo.Bar~~~~0.0.1.0", "amd64", "Foo-Bar-Package")
	if err != nil {
		t.Fatalf("FindCapabilityFile with explicit packageFamily: %v", err)
	}
	if f.Name != "Foo-Bar-Package-amd64.cab" {
		t.Fatalf("got %q", f.Name)
	}
}

func TestFindCapabilityFile_CaseInsensitiveFoDVariance(t *testing.T) {
	// Real file names vary "FOD"/"FoD"/"Fod" case (verified against a real
	// build's file list -- see README.md); matching must tolerate that.
	files := map[string]File{
		"Microsoft-Windows-DNS-Tools-FoD-Package-amd64.cab": {Name: "Microsoft-Windows-DNS-Tools-FoD-Package-amd64.cab"},
	}
	f, err := FindCapabilityFile(files, "Rsat.Dns.Tools~~~~0.0.1.0", "amd64", "Microsoft-Windows-DNS-Tools-FoD-Package")
	if err != nil {
		t.Fatalf("FindCapabilityFile: %v", err)
	}
	if f.Name != "Microsoft-Windows-DNS-Tools-FoD-Package-amd64.cab" {
		t.Fatalf("got %q", f.Name)
	}
}

func TestFindCapabilityFile_NoMatch(t *testing.T) {
	files := map[string]File{
		"Something-Else-amd64.cab": {Name: "Something-Else-amd64.cab"},
	}
	if _, err := FindCapabilityFile(files, "OpenSSH.Server~~~~0.0.1.0", "amd64", ""); err == nil {
		t.Fatal("expected error when no file matches the family/arch")
	}
}
