package driver

import (
	"encoding/binary"
	"testing"
	"testing/fstest"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/service"
)

// registryTestINF extends testINF (driver_test.go) with a hardware ID +
// compatible ID on the Models entry, and a Services/AddService/
// service-install-section chain, so that Services and
// CriticalDeviceDatabaseEntries have something to enumerate alongside the
// existing CopyFiles-driven payload files.
const registryTestINF = `[Version]
Signature="$Windows NT$"
Class=System
ClassGuid={4d36e97d-e325-11ce-bfc1-08002be10318}
Provider=%Mfg%
DriverVer=01/01/2026,1.0.0.0
CatalogFile=contoso.cat

[Manufacturer]
%Mfg%=Contoso,NTamd64

[Contoso.NTamd64]
%DeviceDesc%=Install,ACPI\CONTOSO0001,ACPI\CONTOSO0001_COMPAT

[Install.NTamd64]
CopyFiles=Install.CopyFiles

[Install.NTamd64.Services]
AddService=ContosoDrv,0x00000002,Install.ServiceInstall

[Install.ServiceInstall]
DisplayName    = %function_ServiceDesc%
ServiceType    = 1
StartType      = 3
ErrorControl   = 1
ServiceBinary  = %13%\driver.sys
LoadOrderGroup = Extended Base
Dependencies   = +NetBIOSGroup,RpcSs

[Install.CopyFiles]
driver.sys
helper.dll

[SourceDisksNames]
1 = %DiskDesc%

[SourceDisksFiles]
driver.sys=1
helper.dll=1

[DestinationDirs]
DefaultDestDir=13

[Strings]
Mfg="Contoso, Ltd."
DeviceDesc="Contoso Sample Device"
DiskDesc="Contoso Driver Disk"
function_ServiceDesc="Contoso function driver service"
`

func buildRegistryTestFS(t *testing.T) fstest.MapFS {
	t.Helper()
	sysBytes := buildSysFixture(t)
	dllBytes := []byte("this is not really a PE, just DLL-shaped payload bytes")
	catBytes := buildCatalog(t, sysBytes, dllBytes)

	return fstest.MapFS{
		"contoso.inf": {Data: []byte(registryTestINF)},
		"contoso.cat": {Data: catBytes},
		"driver.sys":  {Data: sysBytes},
		"helper.dll":  {Data: dllBytes},
	}
}

func TestServices(t *testing.T) {
	fsys := buildRegistryTestFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}

	services, err := pkg.Services("amd64")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("got %d services, want 1: %+v", len(services), services)
	}
	svc := services[0]

	if svc.Name != "ContosoDrv" {
		t.Errorf("Name = %q, want ContosoDrv", svc.Name)
	}
	if !svc.AssocService {
		t.Error("AssocService = false, want true (flags 0x00000002 has SPSVCINST_ASSOCSERVICE set)")
	}
	if svc.ServiceType != 1 {
		t.Errorf("ServiceType = %d, want 1", svc.ServiceType)
	}
	if svc.StartType != 3 {
		t.Errorf("StartType = %d, want 3", svc.StartType)
	}
	if svc.ErrorControl != 1 {
		t.Errorf("ErrorControl = %d, want 1", svc.ErrorControl)
	}
	if svc.BinaryDirID != DirIDDriverStore {
		t.Errorf("BinaryDirID = %v, want %v", svc.BinaryDirID, DirIDDriverStore)
	}
	if svc.BinaryPath != "driver.sys" {
		t.Errorf("BinaryPath = %q, want driver.sys", svc.BinaryPath)
	}
	if svc.LoadOrderGroup != "Extended Base" {
		t.Errorf("LoadOrderGroup = %q, want %q", svc.LoadOrderGroup, "Extended Base")
	}
	if len(svc.DependOnGroup) != 1 || svc.DependOnGroup[0] != "NetBIOSGroup" {
		t.Errorf("DependOnGroup = %v, want [NetBIOSGroup]", svc.DependOnGroup)
	}
	if len(svc.DependOnService) != 1 || svc.DependOnService[0] != "RpcSs" {
		t.Errorf("DependOnService = %v, want [RpcSs]", svc.DependOnService)
	}
	if svc.InstallSection != "Install" {
		t.Errorf("InstallSection = %q, want Install", svc.InstallSection)
	}
}

func TestCriticalDeviceDatabaseEntries(t *testing.T) {
	fsys := buildRegistryTestFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}

	entries, err := pkg.CriticalDeviceDatabaseEntries("amd64")
	if err != nil {
		t.Fatalf("CriticalDeviceDatabaseEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}

	byHWID := make(map[string]CriticalDeviceDatabaseEntry)
	for _, e := range entries {
		byHWID[e.HardwareID] = e
	}

	main, ok := byHWID[`ACPI\CONTOSO0001`]
	if !ok {
		t.Fatal(`ACPI\CONTOSO0001 not enumerated`)
	}
	if main.ClassGuid != "{4d36e97d-e325-11ce-bfc1-08002be10318}" {
		t.Errorf("ClassGuid = %q", main.ClassGuid)
	}
	if main.Service != "ContosoDrv" {
		t.Errorf("Service = %q, want ContosoDrv", main.Service)
	}

	compat, ok := byHWID[`ACPI\CONTOSO0001_COMPAT`]
	if !ok {
		t.Fatal(`ACPI\CONTOSO0001_COMPAT not enumerated`)
	}
	if compat.Service != "ContosoDrv" {
		t.Errorf("compatible ID Service = %q, want ContosoDrv", compat.Service)
	}
}

func TestCriticalDeviceDatabaseSubkeyName(t *testing.T) {
	got := CriticalDeviceDatabaseSubkeyName(`ACPI\CONTOSO0001`)
	want := "acpi#contoso0001"
	if got != want {
		t.Errorf("CriticalDeviceDatabaseSubkeyName = %q, want %q", got, want)
	}
}

// --- SYSTEM-hive-shaped *regf.Key fixture ---

// buildSystemHiveRoot hand-builds a minimal SYSTEM-hive-shaped root key:
// Select (with Default=1, REG_DWORD), ControlSet001 (with Services and
// Control\CriticalDeviceDatabase subkeys already present but empty), so that
// CurrentControlSet and InstallRegistry have a realistic tree to resolve
// and merge into.
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
			{
				Name: stringToUTF16LE("Control"),
				Subkeys: []*regf.Key{
					{Name: stringToUTF16LE("CriticalDeviceDatabase")},
				},
			},
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

// CurrentControlSet itself now lives in, and is tested by, the sibling
// service package (service.CurrentControlSet, service/service_test.go);
// driver only relies on it here to build a realistic tree for
// InstallRegistry's tests.

func TestInstallRegistry(t *testing.T) {
	fsys := buildRegistryTestFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}

	root := buildSystemHiveRoot()
	cs, err := service.CurrentControlSet(root)
	if err != nil {
		t.Fatalf("CurrentControlSet: %v", err)
	}

	const destPath = `Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_deadbeef`
	destDirs := map[DirID]string{DirIDDriverStore: destPath}

	if err := InstallRegistry(cs, pkg, destDirs, "amd64"); err != nil {
		t.Fatalf("InstallRegistry: %v", err)
	}

	servicesKey := cs.Subkey("Services")
	if servicesKey == nil {
		t.Fatal("no Services subkey after InstallRegistry")
	}
	svcKey := servicesKey.Subkey("ContosoDrv")
	if svcKey == nil {
		t.Fatal("no Services\\ContosoDrv subkey after InstallRegistry")
	}

	checkDWORD := func(name string, want uint32) {
		v := svcKey.Value(name)
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
	checkDWORD("Type", 1)
	checkDWORD("Start", 3)
	checkDWORD("ErrorControl", 1)

	imagePath := svcKey.Value("ImagePath")
	if imagePath == nil {
		t.Fatal("no ImagePath value")
	}
	if imagePath.Type != regf.RegExpandSZ {
		t.Errorf("ImagePath type = %d, want RegExpandSZ", imagePath.Type)
	}
	wantPath := `\` + destPath + `\driver.sys`
	if got := utf16LEToStringForTest(imagePath.Data); got != wantPath {
		t.Errorf("ImagePath = %q, want %q", got, wantPath)
	}

	group := svcKey.Value("Group")
	if group == nil || utf16LEToStringForTest(group.Data) != "Extended Base" {
		t.Errorf("Group = %+v, want %q", group, "Extended Base")
	}

	dependOnGroup := svcKey.Value("DependOnGroup")
	if dependOnGroup == nil || dependOnGroup.Type != regf.RegMultiSZ {
		t.Fatalf("DependOnGroup missing or wrong type: %+v", dependOnGroup)
	}
	if got := multiSZToStringsForTest(dependOnGroup.Data); len(got) != 1 || got[0] != "NetBIOSGroup" {
		t.Errorf("DependOnGroup = %v, want [NetBIOSGroup]", got)
	}
	dependOnService := svcKey.Value("DependOnService")
	if dependOnService == nil || dependOnService.Type != regf.RegMultiSZ {
		t.Fatalf("DependOnService missing or wrong type: %+v", dependOnService)
	}
	if got := multiSZToStringsForTest(dependOnService.Data); len(got) != 1 || got[0] != "RpcSs" {
		t.Errorf("DependOnService = %v, want [RpcSs]", got)
	}

	controlKey := cs.Subkey("Control")
	if controlKey == nil {
		t.Fatal("no Control subkey")
	}
	cddbKey := controlKey.Subkey("CriticalDeviceDatabase")
	if cddbKey == nil {
		t.Fatal("no Control\\CriticalDeviceDatabase subkey")
	}
	mainSub := cddbKey.Subkey("acpi#contoso0001")
	if mainSub == nil {
		t.Fatal("no CriticalDeviceDatabase\\acpi#contoso0001 subkey")
	}
	classGUID := mainSub.Value("ClassGUID")
	if classGUID == nil || utf16LEToStringForTest(classGUID.Data) != "{4d36e97d-e325-11ce-bfc1-08002be10318}" {
		t.Errorf("ClassGUID = %+v", classGUID)
	}
	svcValue := mainSub.Value("Service")
	if svcValue == nil || utf16LEToStringForTest(svcValue.Data) != "ContosoDrv" {
		t.Errorf("Service = %+v, want ContosoDrv", svcValue)
	}
	if cddbKey.Subkey("acpi#contoso0001_compat") == nil {
		t.Fatal("no CriticalDeviceDatabase\\acpi#contoso0001_compat subkey")
	}

	// InstallRegistry must be idempotent: calling it again against the same
	// tree must not duplicate subkeys or values.
	if err := InstallRegistry(cs, pkg, destDirs, "amd64"); err != nil {
		t.Fatalf("second InstallRegistry: %v", err)
	}
	if len(servicesKey.Subkeys) != 1 {
		t.Fatalf("Services has %d subkeys after reinstall, want 1", len(servicesKey.Subkeys))
	}
	if len(svcKey.Values) != 7 {
		t.Fatalf("Services\\ContosoDrv has %d values after reinstall, want 7: %+v", len(svcKey.Values), svcKey.Values)
	}
	if len(cddbKey.Subkeys) != 2 {
		t.Fatalf("CriticalDeviceDatabase has %d subkeys after reinstall, want 2", len(cddbKey.Subkeys))
	}
	if len(mainSub.Values) != 2 {
		t.Fatalf("CriticalDeviceDatabase\\acpi#contoso0001 has %d values after reinstall, want 2", len(mainSub.Values))
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

	parsedCS, err := service.CurrentControlSet(parsed.Root)
	if err != nil {
		t.Fatalf("CurrentControlSet on round-tripped hive: %v", err)
	}
	parsedServices := parsedCS.Subkey("Services")
	if parsedServices == nil || parsedServices.Subkey("ContosoDrv") == nil {
		t.Fatal("round-tripped hive missing Services\\ContosoDrv")
	}
	parsedControl := parsedCS.Subkey("Control")
	if parsedControl == nil {
		t.Fatal("round-tripped hive missing Control")
	}
	parsedCDDB := parsedControl.Subkey("CriticalDeviceDatabase")
	if parsedCDDB == nil || parsedCDDB.Subkey("acpi#contoso0001") == nil {
		t.Fatal("round-tripped hive missing CriticalDeviceDatabase\\acpi#contoso0001")
	}
}

func TestInstallRegistryMissingDestDir(t *testing.T) {
	fsys := buildRegistryTestFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	root := buildSystemHiveRoot()
	cs, err := service.CurrentControlSet(root)
	if err != nil {
		t.Fatalf("CurrentControlSet: %v", err)
	}
	if err := InstallRegistry(cs, pkg, map[DirID]string{}, "amd64"); err == nil {
		t.Fatal("expected an error when no destination directory is supplied for the service binary's DIRID")
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
	return string(utf16DecodeForTest(u16))
}

func utf16DecodeForTest(u16 []uint16) []rune {
	// Minimal UTF-16 decode sufficient for the plain-ASCII strings used in
	// this test file; avoids importing unicode/utf16 solely for test code
	// when bytes.Equal-style byte comparison would otherwise suffice, but a
	// string comparison reads more clearly for the assertions above.
	out := make([]rune, 0, len(u16))
	for i := 0; i < len(u16); i++ {
		out = append(out, rune(u16[i]))
	}
	return out
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
