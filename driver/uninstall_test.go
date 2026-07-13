package driver

import (
	"testing"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/wim"
)

// buildInstalledFileRepository hand-builds a FileRepository *wim.DirEntry
// containing two synthetic driver package folders ("contoso.inf_amd64_dead"
// and "other.inf_amd64_beef"), each with a single "driver.sys" file, plus a
// shared blob table with the corresponding blob-table entries. This mirrors
// the shape Install would have produced, without needing a full
// LoadPackage/Install round trip.
func buildInstalledFileRepository() (fileRepo *wim.DirEntry, bt *wim.BlobTable, contosoHash, otherHash wim.Hash) {
	contosoHash = wim.Hash{0x01}
	otherHash = wim.Hash{0x02}

	mkPkgDir := func(name string, hash wim.Hash) *wim.DirEntry {
		return &wim.DirEntry{
			Attributes: wim.FileAttributeDirectory,
			SecurityID: wim.SecurityIDNone,
			Name:       stringToUTF16LE(name),
			Children: []*wim.DirEntry{
				{
					SecurityID: wim.SecurityIDNone,
					Name:       stringToUTF16LE("driver.sys"),
					Streams:    []wim.Stream{{Hash: hash}},
				},
			},
		}
	}

	contosoDir := mkPkgDir("contoso.inf_amd64_dead", contosoHash)
	otherDir := mkPkgDir("other.inf_amd64_beef", otherHash)

	fileRepo = &wim.DirEntry{
		Attributes: wim.FileAttributeDirectory,
		SecurityID: wim.SecurityIDNone,
		Name:       stringToUTF16LE("FileRepository"),
		Children:   []*wim.DirEntry{contosoDir, otherDir},
	}

	bt = &wim.BlobTable{
		Entries: []wim.BlobDescriptor{
			{Hash: contosoHash, RefCount: 1},
			{Hash: otherHash, RefCount: 2},
		},
	}

	return fileRepo, bt, contosoHash, otherHash
}

func TestListInstalled(t *testing.T) {
	fileRepo, _, _, _ := buildInstalledFileRepository()

	pkgs, err := ListInstalled(fileRepo)
	if err != nil {
		t.Fatalf("ListInstalled: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d installed packages, want 2: %+v", len(pkgs), pkgs)
	}
	if pkgs[0].Name != "contoso.inf_amd64_dead" {
		t.Errorf("pkgs[0].Name = %q, want contoso.inf_amd64_dead", pkgs[0].Name)
	}
	if pkgs[0].Dir != fileRepo.Children[0] {
		t.Error("pkgs[0].Dir does not point back to the same DirEntry")
	}
	if pkgs[1].Name != "other.inf_amd64_beef" {
		t.Errorf("pkgs[1].Name = %q, want other.inf_amd64_beef", pkgs[1].Name)
	}

	// A non-directory child directly under FileRepository (which should
	// never legitimately occur) is silently skipped, not an error.
	fileRepo.Children = append(fileRepo.Children, &wim.DirEntry{
		SecurityID: wim.SecurityIDNone,
		Name:       stringToUTF16LE("stray.txt"),
		Streams:    []wim.Stream{{}},
	})
	pkgs, err = ListInstalled(fileRepo)
	if err != nil {
		t.Fatalf("ListInstalled with stray file: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d installed packages with a stray file present, want 2: %+v", len(pkgs), pkgs)
	}
}

func TestListInstalledNilFileRepository(t *testing.T) {
	if _, err := ListInstalled(nil); err == nil {
		t.Fatal("expected an error for a nil FileRepository directory")
	}
}

// buildUninstallSystemHiveRoot hand-builds a minimal SYSTEM-hive-shaped root
// key (mirroring driver_registry_test.go's buildSystemHiveRoot) with a
// Services\ContosoDrv key and two CriticalDeviceDatabase entries: one
// belonging to ContosoDrv, and one belonging to an unrelated OtherDrv
// service, so Uninstall's tests can assert only the former is removed.
func buildUninstallSystemHiveRoot() *regf.Key {
	servicesKey := &regf.Key{
		Name: stringToUTF16LE("Services"),
		Subkeys: []*regf.Key{
			{
				Name: stringToUTF16LE("ContosoDrv"),
				Values: []regf.Value{
					{Name: stringToUTF16LE("Type"), Type: regf.RegDWORD, Data: []byte{1, 0, 0, 0}},
				},
			},
		},
	}

	cddbKey := &regf.Key{
		Name: stringToUTF16LE("CriticalDeviceDatabase"),
		Subkeys: []*regf.Key{
			{
				Name: stringToUTF16LE("acpi#contoso0001"),
				Values: []regf.Value{
					{Name: stringToUTF16LE("Service"), Type: regf.RegSZ, Data: stringToUTF16LE("ContosoDrv")},
				},
			},
			{
				Name: stringToUTF16LE("acpi#otherdev0001"),
				Values: []regf.Value{
					{Name: stringToUTF16LE("Service"), Type: regf.RegSZ, Data: stringToUTF16LE("OtherDrv")},
				},
			},
		},
	}

	controlSet001 := &regf.Key{
		Name: stringToUTF16LE("ControlSet001"),
		Subkeys: []*regf.Key{
			servicesKey,
			{
				Name:    stringToUTF16LE("Control"),
				Subkeys: []*regf.Key{cddbKey},
			},
		},
	}

	return controlSet001
}

func TestUninstall(t *testing.T) {
	fileRepo, bt, contosoHash, otherHash := buildInstalledFileRepository()
	cs := buildUninstallSystemHiveRoot()

	if err := Uninstall(bt, cs, fileRepo, "contoso.inf_amd64_dead", "ContosoDrv"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// The DirEntry subtree is gone.
	if len(fileRepo.Children) != 1 {
		t.Fatalf("FileRepository has %d children after Uninstall, want 1: %+v", len(fileRepo.Children), fileRepo.Children)
	}
	if fileRepo.Children[0].NameUTF8() != "other.inf_amd64_beef" {
		t.Fatalf("remaining child = %q, want other.inf_amd64_beef", fileRepo.Children[0].NameUTF8())
	}

	// The removed subtree's own blob had its RefCount decremented; the
	// unrelated blob (still referenced by the surviving package) is
	// untouched.
	var gotContosoRef, gotOtherRef uint32
	for _, e := range bt.Entries {
		switch e.Hash {
		case contosoHash:
			gotContosoRef = e.RefCount
		case otherHash:
			gotOtherRef = e.RefCount
		}
	}
	if gotContosoRef != 0 {
		t.Errorf("contoso blob RefCount = %d, want 0", gotContosoRef)
	}
	if gotOtherRef != 2 {
		t.Errorf("other blob RefCount = %d, want unchanged 2", gotOtherRef)
	}
	if len(bt.Entries) != 2 {
		t.Errorf("blob table has %d entries, want 2 (Uninstall must not delete the entry itself)", len(bt.Entries))
	}

	// The Services\ContosoDrv key is gone.
	servicesKey := cs.Subkey("Services")
	if servicesKey == nil {
		t.Fatal("no Services subkey")
	}
	if servicesKey.Subkey("ContosoDrv") != nil {
		t.Fatal("Services\\ContosoDrv still present after Uninstall")
	}

	// Only ContosoDrv's CriticalDeviceDatabase entry was removed; OtherDrv's
	// is untouched.
	controlKey := cs.Subkey("Control")
	if controlKey == nil {
		t.Fatal("no Control subkey")
	}
	cddbKey := controlKey.Subkey("CriticalDeviceDatabase")
	if cddbKey == nil {
		t.Fatal("no CriticalDeviceDatabase subkey")
	}
	if len(cddbKey.Subkeys) != 1 {
		t.Fatalf("CriticalDeviceDatabase has %d subkeys after Uninstall, want 1: %+v", len(cddbKey.Subkeys), cddbKey.Subkeys)
	}
	if cddbKey.Subkeys[0].NameUTF8() != "acpi#otherdev0001" {
		t.Errorf("remaining CDDB subkey = %q, want acpi#otherdev0001", cddbKey.Subkeys[0].NameUTF8())
	}
}

func TestUninstallAlreadyGone(t *testing.T) {
	// Calling Uninstall against a target that has already been fully
	// removed (or was never installed) must succeed rather than error - see
	// Uninstall's doc comment's "already gone is success" reasoning.
	fileRepo, bt, _, _ := buildInstalledFileRepository()
	cs := buildUninstallSystemHiveRoot()

	if err := Uninstall(bt, cs, fileRepo, "nonexistent.inf_amd64_0000", "NoSuchService"); err != nil {
		t.Fatalf("Uninstall on an already-absent package: %v", err)
	}
	if len(fileRepo.Children) != 2 {
		t.Fatalf("FileRepository children changed for an unrelated Uninstall call: %d, want 2", len(fileRepo.Children))
	}

	// Uninstall must also be idempotent when called twice against the same
	// (now-removed) driver.
	if err := Uninstall(bt, cs, fileRepo, "contoso.inf_amd64_dead", "ContosoDrv"); err != nil {
		t.Fatalf("first Uninstall: %v", err)
	}
	if err := Uninstall(bt, cs, fileRepo, "contoso.inf_amd64_dead", "ContosoDrv"); err != nil {
		t.Fatalf("second Uninstall on already-removed driver: %v", err)
	}
}

func TestUninstallNilCurrentControlSet(t *testing.T) {
	// A nil currentControlSet must still let the DriverStore-side removal
	// happen; only the registry steps are skipped.
	fileRepo, bt, contosoHash, _ := buildInstalledFileRepository()

	if err := Uninstall(bt, nil, fileRepo, "contoso.inf_amd64_dead", "ContosoDrv"); err != nil {
		t.Fatalf("Uninstall with nil currentControlSet: %v", err)
	}
	if len(fileRepo.Children) != 1 {
		t.Fatalf("FileRepository has %d children, want 1", len(fileRepo.Children))
	}
	for _, e := range bt.Entries {
		if e.Hash == contosoHash && e.RefCount != 0 {
			t.Errorf("contoso blob RefCount = %d, want 0", e.RefCount)
		}
	}
}

func TestUninstallRequiredArgs(t *testing.T) {
	fileRepo, bt, _, _ := buildInstalledFileRepository()
	cs := buildUninstallSystemHiveRoot()

	if err := Uninstall(bt, cs, nil, "contoso.inf_amd64_dead", "ContosoDrv"); err == nil {
		t.Error("expected an error for a nil driverStoreParent")
	}
	if err := Uninstall(bt, cs, fileRepo, "", "ContosoDrv"); err == nil {
		t.Error("expected an error for an empty driverStoreDirName")
	}
	if err := Uninstall(bt, cs, fileRepo, "contoso.inf_amd64_dead", ""); err == nil {
		t.Error("expected an error for an empty serviceName")
	}
}
