package service

import (
	"encoding/binary"
	"testing"

	"github.com/gavin-john/gowim/regf"
)

// buildSystemHiveRoot hand-builds a minimal SYSTEM-hive-shaped root key:
// Select (with Default=1, REG_DWORD), ControlSet001 (with an empty Services
// subkey already present), so that CurrentControlSet and Install have a
// realistic tree to resolve and merge into.
func buildSystemHiveRoot() *regf.Key {
	dword := func(v uint32) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, v)
		return b
	}

	controlSet001 := &regf.Key{
		Name: stringToUTF16LE("ControlSet001"),
		Subkeys: []*regf.Key{
			{Name: stringToUTF16LE("Services")},
		},
	}

	selectKey := &regf.Key{
		Name: stringToUTF16LE("Select"),
		Values: []regf.Value{
			{Name: stringToUTF16LE("Default"), Type: regf.RegDWORD, Data: dword(1)},
			{Name: stringToUTF16LE("Current"), Type: regf.RegDWORD, Data: dword(1)},
			{Name: stringToUTF16LE("LastKnownGood"), Type: regf.RegDWORD, Data: dword(1)},
			{Name: stringToUTF16LE("Failed"), Type: regf.RegDWORD, Data: dword(0)},
		},
	}

	return &regf.Key{
		Flags:   regf.KeyFlagHiveEntry,
		Subkeys: []*regf.Key{selectKey, controlSet001},
	}
}

func TestCurrentControlSet(t *testing.T) {
	root := buildSystemHiveRoot()

	cs, err := CurrentControlSet(root)
	if err != nil {
		t.Fatalf("CurrentControlSet: %v", err)
	}
	if cs.NameUTF8() != "ControlSet001" {
		t.Errorf("resolved control set = %q, want ControlSet001", cs.NameUTF8())
	}
}

func TestCurrentControlSetErrors(t *testing.T) {
	if _, err := CurrentControlSet(nil); err == nil {
		t.Error("expected an error for a nil SYSTEM root")
	}

	noSelect := &regf.Key{Flags: regf.KeyFlagHiveEntry}
	if _, err := CurrentControlSet(noSelect); err == nil {
		t.Error("expected an error when no Select subkey exists")
	}

	noDefault := &regf.Key{
		Flags:   regf.KeyFlagHiveEntry,
		Subkeys: []*regf.Key{{Name: stringToUTF16LE("Select")}},
	}
	if _, err := CurrentControlSet(noDefault); err == nil {
		t.Error("expected an error when Select has no Default value")
	}
}

func TestInstall(t *testing.T) {
	root := buildSystemHiveRoot()
	cs, err := CurrentControlSet(root)
	if err != nil {
		t.Fatalf("CurrentControlSet: %v", err)
	}
	servicesKey := FindOrCreateSubkey(cs, "Services")

	svc := Service{
		Name:            "ContosoDrv",
		Type:            TypeKernelDriver,
		Start:           StartDemand,
		ErrorControl:    ErrorNormal,
		ImagePath:       `\Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_deadbeef\driver.sys`,
		Group:           "Extended Base",
		DependOnGroup:   []string{"NetBIOSGroup"},
		DependOnService: []string{"RpcSs"},
	}

	if err := Install(servicesKey, svc); err != nil {
		t.Fatalf("Install: %v", err)
	}

	svcKey := FindSubkey(servicesKey, "ContosoDrv")
	if svcKey == nil {
		t.Fatal("no Services\\ContosoDrv subkey after Install")
	}

	checkDWORD := func(name string, want uint32) {
		v := FindValue(svcKey, name)
		if v == nil {
			t.Fatalf("no %s value under Services\\ContosoDrv", name)
		}
		if v.Type != regf.RegDWORD {
			t.Fatalf("%s type = %d, want RegDWORD", name, v.Type)
		}
		if len(v.Data) < 4 || binary.LittleEndian.Uint32(v.Data[:4]) != want {
			t.Fatalf("%s = % x, want %d", name, v.Data, want)
		}
	}
	checkDWORD("Type", TypeKernelDriver)
	checkDWORD("Start", StartDemand)
	checkDWORD("ErrorControl", ErrorNormal)

	imagePath := FindValue(svcKey, "ImagePath")
	if imagePath == nil {
		t.Fatal("no ImagePath value")
	}
	if imagePath.Type != regf.RegExpandSZ {
		t.Errorf("ImagePath type = %d, want RegExpandSZ", imagePath.Type)
	}
	if got := utf16LEToStringForTest(imagePath.Data); got != svc.ImagePath {
		t.Errorf("ImagePath = %q, want %q", got, svc.ImagePath)
	}

	group := FindValue(svcKey, "Group")
	if group == nil || utf16LEToStringForTest(group.Data) != "Extended Base" {
		t.Errorf("Group = %+v, want %q", group, "Extended Base")
	}

	dependOnGroup := FindValue(svcKey, "DependOnGroup")
	if dependOnGroup == nil || dependOnGroup.Type != regf.RegMultiSZ {
		t.Fatalf("DependOnGroup missing or wrong type: %+v", dependOnGroup)
	}
	if got := multiSZToStringsForTest(dependOnGroup.Data); len(got) != 1 || got[0] != "NetBIOSGroup" {
		t.Errorf("DependOnGroup = %v, want [NetBIOSGroup]", got)
	}
	dependOnService := FindValue(svcKey, "DependOnService")
	if dependOnService == nil || dependOnService.Type != regf.RegMultiSZ {
		t.Fatalf("DependOnService missing or wrong type: %+v", dependOnService)
	}
	if got := multiSZToStringsForTest(dependOnService.Data); len(got) != 1 || got[0] != "RpcSs" {
		t.Errorf("DependOnService = %v, want [RpcSs]", got)
	}

	// Install must be idempotent: calling it again against the same tree
	// must not duplicate subkeys or values.
	if err := Install(servicesKey, svc); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(servicesKey.Subkeys) != 1 {
		t.Fatalf("Services has %d subkeys after reinstall, want 1", len(servicesKey.Subkeys))
	}
	if len(svcKey.Values) != 7 {
		t.Fatalf("Services\\ContosoDrv has %d values after reinstall, want 7: %+v", len(svcKey.Values), svcKey.Values)
	}

	// Re-installing without Group/DependOnGroup/DependOnService must clear
	// the previously-set values rather than leaving them stale.
	svc2 := svc
	svc2.Group = ""
	svc2.DependOnGroup = nil
	svc2.DependOnService = nil
	if err := Install(servicesKey, svc2); err != nil {
		t.Fatalf("third Install (clearing optional fields): %v", err)
	}
	if FindValue(svcKey, "Group") != nil {
		t.Error("Group value still present after re-installing without a Group")
	}
	if FindValue(svcKey, "DependOnGroup") != nil {
		t.Error("DependOnGroup value still present after re-installing without DependOnGroup")
	}
	if FindValue(svcKey, "DependOnService") != nil {
		t.Error("DependOnService value still present after re-installing without DependOnService")
	}
	if len(svcKey.Values) != 4 {
		t.Fatalf("Services\\ContosoDrv has %d values after clearing optional fields, want 4: %+v", len(svcKey.Values), svcKey.Values)
	}

	// The merged tree must round-trip through regf.Hive.AppendTo/regf.Parse.
	hive := &regf.Hive{
		BaseBlock: regf.BaseBlock{MajorVersion: 1, MinorVersion: regf.Version1_5, ClusteringFactor: 1},
		Root:      root,
	}
	data, err := hive.AppendTo(nil)
	if err != nil {
		t.Fatalf("Hive.AppendTo: %v", err)
	}
	parsed, err := regf.Parse(data)
	if err != nil {
		t.Fatalf("regf.Parse: %v", err)
	}
	parsedCS, err := CurrentControlSet(parsed.Root)
	if err != nil {
		t.Fatalf("CurrentControlSet on round-tripped hive: %v", err)
	}
	parsedServices := FindSubkey(parsedCS, "Services")
	if parsedServices == nil || FindSubkey(parsedServices, "ContosoDrv") == nil {
		t.Fatal("round-tripped hive missing Services\\ContosoDrv")
	}
}

func TestInstallErrors(t *testing.T) {
	root := buildSystemHiveRoot()
	cs, err := CurrentControlSet(root)
	if err != nil {
		t.Fatalf("CurrentControlSet: %v", err)
	}
	servicesKey := FindOrCreateSubkey(cs, "Services")

	if err := Install(nil, Service{Name: "X", ImagePath: `\x`}); err == nil {
		t.Error("expected an error for a nil services key")
	}
	if err := Install(servicesKey, Service{ImagePath: `\x`}); err == nil {
		t.Error("expected an error for a service with no name")
	}
	if err := Install(servicesKey, Service{Name: "X"}); err == nil {
		t.Error("expected an error for a service with no ImagePath")
	}
}

// utf16LEToStringForTest decodes a UTF-16LE (no BOM, no terminator) byte
// slice, for asserting on REG_SZ/REG_EXPAND_SZ value Data in tests.
func utf16LEToStringForTest(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	out := make([]rune, len(u16))
	for i, c := range u16 {
		out[i] = rune(c)
	}
	return string(out)
}

// multiSZToStringsForTest splits a REG_MULTI_SZ value's Data into its
// component strings.
func multiSZToStringsForTest(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			if i > start {
				out = append(out, utf16LEToStringForTest(b[start:i]))
			}
			start = i + 2
		}
	}
	return out
}
