package component

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Pandapip1/gowim/regf"
)

// newTestHiveRoot builds an empty hive root key of the shape regf.Parse
// produces, so InstallRegistry's find-or-create writes exercise the real
// regf.Key API rather than a stand-in.
func newTestHiveRoot() *regf.Key {
	return &regf.Key{Name: stringToUTF16LE("ROOT")}
}

func valueOf(t *testing.T, k *regf.Key, name string) *regf.Value {
	t.Helper()
	if k == nil {
		t.Fatalf("nil key while looking up value %q", name)
	}
	v := k.Value(name)
	if v == nil {
		var have []string
		for i := range k.Values {
			have = append(have, k.Values[i].NameUTF8())
		}
		t.Fatalf("value %q not found; key has %v", name, have)
	}
	return v
}

// szz decodes a NUL-terminated REG_SZ the way Windows writes it, which is
// what encodeSZZ produces (regf.Value.SZ does not strip the terminator).
func szz(v *regf.Value) string {
	u16 := make([]uint16, 0, len(v.Data)/2)
	for i := 0; i+1 < len(v.Data); i += 2 {
		u16 = append(u16, uint16(v.Data[i])|uint16(v.Data[i+1])<<8)
	}
	return strings.TrimRight(string(utf16.Decode(u16)), "\x00")
}

func serviceableFixture() *Installation {
	catalog := []byte("catalog bytes")
	return &Installation{
		Serviceability: Serviceable,
		Components: []ComponentInstall{{
			KeyForm:     fixtureKeyForm,
			Manifest:    fixturePlainManifest,
			Files:       []PayloadFile{{Name: "comctl32.dll", Data: []byte("payload")}},
			Deployments: []string{"microsoft-w..ployment050_31bf3856ad364e35_10.0.26100.8037_aa6931116f1f24c1"},
		}},
		Deployments: []DeploymentInstall{{
			KeyName: "microsoft-w..ployment050_31bf3856ad364e35_10.0.26100.8037_aa6931116f1f24c1",
			AppID: "microsoft-windows-client-desktop-required-deployment050, Culture=neutral, " +
				"Version=10.0.26100.8037, PublicKeyToken=31bf3856ad364e35, ProcessorArchitecture=amd64",
			CatalogThumbprint: CatalogThumbprint(catalog),
		}},
		Packages: []PackageInstall{{
			Name:    "Test-Package~31bf3856ad364e35~amd64~~1.0.0.0",
			MUM:     []byte("<assembly xmlns=\"urn:schemas-microsoft-com:asm.v3\"/>"),
			Catalog: catalog,
			Owners:  []string{"Parent-Package~31bf3856ad364e35~amd64~~1.0.0.0"},
		}},
	}
}

func TestInstallRegistryRefusesBuildOnce(t *testing.T) {
	inst := serviceableFixture()
	inst.Serviceability = BuildOnce
	err := InstallRegistry(&Hives{Components: newTestHiveRoot(), Software: newTestHiveRoot()}, inst)
	if !errors.Is(err, ErrBuildOnce) {
		t.Fatalf("InstallRegistry for a BuildOnce installation: err = %v, want ErrBuildOnce", err)
	}
}

func TestInstallRegistryRefusesUnset(t *testing.T) {
	inst := serviceableFixture()
	inst.Serviceability = ServiceabilityUnset
	err := InstallRegistry(&Hives{Components: newTestHiveRoot(), Software: newTestHiveRoot()}, inst)
	if !errors.Is(err, ErrServiceabilityUnset) {
		t.Fatalf("err = %v, want ErrServiceabilityUnset", err)
	}
}

func TestInstallRegistryNeedsComponentsHive(t *testing.T) {
	err := InstallRegistry(&Hives{Software: newTestHiveRoot()}, serviceableFixture())
	if err == nil {
		t.Fatal("InstallRegistry accepted a Serviceable installation with no COMPONENTS hive")
	}
}

func TestInstallRegistryComponentKey(t *testing.T) {
	comps, software := newTestHiveRoot(), newTestHiveRoot()
	inst := serviceableFixture()
	if err := InstallRegistry(&Hives{Components: comps, Software: software}, inst); err != nil {
		t.Fatalf("InstallRegistry: %v", err)
	}

	key := comps.OpenPath(ComponentsKeyPath + `\` + fixtureKeyForm)
	if key == nil {
		t.Fatalf("no %s\\%s key", ComponentsKeyPath, fixtureKeyForm)
	}

	// identity: REG_BINARY, ASCII, NOT NUL-terminated -- 28069 of 28069 real
	// values are unterminated.
	idv := valueOf(t, key, "identity")
	if idv.Type != regf.RegBinary {
		t.Errorf("identity type = %d, want REG_BINARY (%d)", idv.Type, regf.RegBinary)
	}
	if string(idv.Data) != fixtureIdentity {
		t.Errorf("identity = %q, want %q", idv.Data, fixtureIdentity)
	}
	if bytes.HasSuffix(idv.Data, []byte{0}) {
		t.Error("identity is NUL-terminated; real hive values are not")
	}

	// S256H: REG_BINARY, 32 bytes, the real image's value for this manifest.
	s := valueOf(t, key, "S256H")
	if s.Type != regf.RegBinary || len(s.Data) != 32 {
		t.Errorf("S256H type=%d len=%d, want REG_BINARY and 32 bytes", s.Type, len(s.Data))
	}
	if got := hex.EncodeToString(s.Data); got != fixtureS256H {
		t.Errorf("S256H = %s, want the real hive's %s", got, fixtureS256H)
	}

	// f!<file>: REG_DWORD, name verbatim (real key carries f!comctl32.dll).
	f := valueOf(t, key, "f!comctl32.dll")
	if f.Type != regf.RegDWORD {
		t.Errorf("f!comctl32.dll type = %d, want REG_DWORD (%d)", f.Type, regf.RegDWORD)
	}

	// c!<deployment>: REG_BINARY, zero length.
	c := valueOf(t, key, "c!microsoft-w..ployment050_31bf3856ad364e35_10.0.26100.8037_aa6931116f1f24c1")
	if c.Type != regf.RegBinary || len(c.Data) != 0 {
		t.Errorf("c! value type=%d len=%d, want REG_BINARY and zero length", c.Type, len(c.Data))
	}

	// CF is sparse in real images (17127 of 28069 have none) and nil here.
	if key.Value("CF") != nil {
		t.Error("CF written even though ComponentInstall.CF is nil")
	}
}

func TestInstallRegistryComponentCF(t *testing.T) {
	comps := newTestHiveRoot()
	inst := serviceableFixture()
	cf := uint32(0x00080000)
	inst.Components[0].CF = &cf
	inst.Packages = nil
	inst.Deployments = nil
	inst.Components[0].Deployments = nil
	if err := InstallRegistry(&Hives{Components: comps}, inst); err != nil {
		t.Fatalf("InstallRegistry: %v", err)
	}
	v := valueOf(t, comps.OpenPath(ComponentsKeyPath+`\`+fixtureKeyForm), "CF")
	got, err := v.DWORD()
	if err != nil || got != cf {
		t.Errorf("CF = %#x (err %v), want %#x", got, err, cf)
	}
}

func TestInstallRegistryRejectsTruncatedFileValueName(t *testing.T) {
	comps := newTestHiveRoot()
	inst := serviceableFixture()
	inst.Packages = nil
	// 26 characters: one past the measured verbatim boundary.
	inst.Components[0].Files = []PayloadFile{{Name: "microsoft.wsman.managementx", Data: []byte("x")}}
	err := InstallRegistry(&Hives{Components: comps}, inst)
	if err == nil {
		t.Fatal("InstallRegistry accepted a payload file name past the verbatim f! boundary")
	}
	if !strings.Contains(err.Error(), "truncates") {
		t.Errorf("error does not explain the truncation problem: %v", err)
	}
}

func TestFileValueNameBoundary(t *testing.T) {
	// 25 is the longest verbatim name observed across all 28069 components;
	// 26 is the shortest length at which none is ever verbatim.
	if _, err := fileValueName(strings.Repeat("a", 25)); err != nil {
		t.Errorf("25-character name rejected: %v", err)
	}
	if _, err := fileValueName(strings.Repeat("a", 26)); err == nil {
		t.Error("26-character name accepted")
	}
	// The real observed pair, for good measure.
	if n, err := fileValueName("AssignedAccessRuntime.dll"); err != nil || n != "f!AssignedAccessRuntime.dll" {
		t.Errorf("fileValueName(real 25-char name) = %q, %v", n, err)
	}
}

func TestInstallRegistryDeploymentAndCatalogKeys(t *testing.T) {
	comps, software := newTestHiveRoot(), newTestHiveRoot()
	inst := serviceableFixture()
	if err := InstallRegistry(&Hives{Components: comps, Software: software}, inst); err != nil {
		t.Fatalf("InstallRegistry: %v", err)
	}

	depName := inst.Deployments[0].KeyName
	dep := comps.OpenPath(DeploymentsKeyPath + `\` + depName)
	if dep == nil {
		t.Fatalf("no deployment key %q", depName)
	}
	if got := string(valueOf(t, dep, "appid").Data); got != inst.Deployments[0].AppID {
		t.Errorf("appid = %q, want %q", got, inst.Deployments[0].AppID)
	}
	tp := valueOf(t, dep, "CatalogThumbprint")
	if tp.Type != regf.RegSZ {
		t.Errorf("CatalogThumbprint type = %d, want REG_SZ (%d)", tp.Type, regf.RegSZ)
	}
	// Real values are NUL-terminated: a 64-character thumbprint is 130
	// bytes, not 128.
	if len(tp.Data) != 130 {
		t.Errorf("CatalogThumbprint data length = %d, want 130 (64 UTF-16 chars + NUL)", len(tp.Data))
	}
	if szz(tp) != inst.Deployments[0].CatalogThumbprint {
		t.Errorf("CatalogThumbprint = %q, want %q", szz(tp), inst.Deployments[0].CatalogThumbprint)
	}

	// The deployment key name's computable prefix must agree with its appid,
	// the same cross-check that holds for all 3983 real deployments.
	appidName := inst.Deployments[0].AppID[:strings.Index(inst.Deployments[0].AppID, ",")]
	if got := DeploymentKeyNamePrefix(appidName); !strings.HasPrefix(depName, got+"_") {
		t.Errorf("deployment key name %q does not start with the prefix %q derived from its appid", depName, got)
	}

	// Catalog key: named by the SHA-256 of the catalog bytes, back-linking
	// to the deployment that names that thumbprint.
	thumb := CatalogThumbprint(inst.Packages[0].Catalog)
	cat := comps.OpenPath(CatalogsKeyPath + `\` + thumb)
	if cat == nil {
		t.Fatalf("no catalog key %q", thumb)
	}
	if v := valueOf(t, cat, "c!"+depName); v.Type != regf.RegBinary || len(v.Data) != 0 {
		t.Errorf("catalog c! value type=%d len=%d, want REG_BINARY zero-length", v.Type, len(v.Data))
	}
}

func TestInstallRegistryPackageKey(t *testing.T) {
	comps, software := newTestHiveRoot(), newTestHiveRoot()
	inst := serviceableFixture()
	if err := InstallRegistry(&Hives{Components: comps, Software: software}, inst); err != nil {
		t.Fatalf("InstallRegistry: %v", err)
	}
	name := inst.Packages[0].Name
	key := software.OpenPath(CBSPackagesKeyPath + `\` + name)
	if key == nil {
		t.Fatalf("no package key %q", name)
	}
	// Measured invariant: InstallName is exactly the key name plus ".mum",
	// for all 3517 package keys in the real image.
	if got := szz(valueOf(t, key, "InstallName")); got != name+".mum" {
		t.Errorf("InstallName = %q, want %q", got, name+".mum")
	}
	if got := szz(valueOf(t, key, "InstallClient")); got != DefaultInstallClient {
		t.Errorf("InstallClient = %q, want %q", got, DefaultInstallClient)
	}
	if got, _ := valueOf(t, key, "CurrentState").DWORD(); got != PackageStateInstalled {
		t.Errorf("CurrentState = %#x, want %#x", got, PackageStateInstalled)
	}
	if got, _ := valueOf(t, key, "Visibility").DWORD(); got != VisibilityDefault {
		t.Errorf("Visibility = %d, want %d", got, VisibilityDefault)
	}
	if got, _ := valueOf(t, key, "SelfUpdate").DWORD(); got != 0 {
		t.Errorf("SelfUpdate = %d, want 0", got)
	}
	owners := key.Subkey("Owners")
	if owners == nil {
		t.Fatal("no Owners subkey")
	}
	if valueOf(t, owners, inst.Packages[0].Owners[0]).Type != regf.RegDWORD {
		t.Error("Owners entry is not a REG_DWORD")
	}
}

func TestInstallRegistryIsIdempotent(t *testing.T) {
	comps, software := newTestHiveRoot(), newTestHiveRoot()
	inst := serviceableFixture()
	for i := 0; i < 2; i++ {
		if err := InstallRegistry(&Hives{Components: comps, Software: software}, inst); err != nil {
			t.Fatalf("InstallRegistry #%d: %v", i+1, err)
		}
	}
	key := comps.OpenPath(ComponentsKeyPath + `\` + fixtureKeyForm)
	if n := len(key.Values); n != 4 {
		var have []string
		for i := range key.Values {
			have = append(have, key.Values[i].NameUTF8())
		}
		t.Errorf("component key has %d values after two calls, want 4: %v", n, have)
	}
	parent := comps.OpenPath(ComponentsKeyPath)
	if n := len(parent.Subkeys); n != 1 {
		t.Errorf("%s has %d subkeys after two calls, want 1", ComponentsKeyPath, n)
	}
}

// TestInstallRegistrySurvivesSerialization writes the mutated hive out with
// regf's own writer and reads it back, so the values are proven to survive a
// real hive round-trip rather than only existing in memory -- the same trip
// the sibling registry package's Hive.Save makes.
func TestInstallRegistrySurvivesSerialization(t *testing.T) {
	comps := newTestHiveRoot()
	inst := serviceableFixture()
	inst.Packages = nil
	if err := InstallRegistry(&Hives{Components: comps}, inst); err != nil {
		t.Fatalf("InstallRegistry: %v", err)
	}
	h := &regf.Hive{Root: comps}
	data, err := h.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	back, err := regf.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	key := back.Root.OpenPath(ComponentsKeyPath + `\` + fixtureKeyForm)
	if key == nil {
		t.Fatal("component key did not survive serialization")
	}
	if got := hex.EncodeToString(valueOf(t, key, "S256H").Data); got != fixtureS256H {
		t.Errorf("S256H after round-trip = %s, want %s", got, fixtureS256H)
	}
	if got := string(valueOf(t, key, "identity").Data); got != fixtureIdentity {
		t.Errorf("identity after round-trip = %q", got)
	}
}

func TestInstallWinners(t *testing.T) {
	software := newTestHiveRoot()
	// Real Winners key name and values for Common-Controls in the measured
	// image: note the keyform is version-less and its trailing hash differs
	// from the component's.
	err := InstallWinners(software, []WinnersInstall{{
		KeyForm:       "amd64_microsoft.windows.common-controls_6595b64144ccf1df_none_62fe57338acfab7a",
		VersionFamily: "5.82",
		Version:       "5.82.26100.8037",
	}, {
		KeyForm:       "amd64_microsoft.windows.common-controls_6595b64144ccf1df_none_62fe57338acfab7a",
		VersionFamily: "6.0",
		Version:       "6.0.26100.8037",
	}})
	if err != nil {
		t.Fatalf("InstallWinners: %v", err)
	}
	base := software.OpenPath(WinnersKeyPath +
		`\amd64_microsoft.windows.common-controls_6595b64144ccf1df_none_62fe57338acfab7a`)
	if base == nil {
		t.Fatal("no Winners key")
	}
	if len(base.Subkeys) != 2 {
		t.Fatalf("Winners key has %d version-family subkeys, want 2", len(base.Subkeys))
	}
	fam := base.Subkey("6.0")
	if got := szz(valueOf(t, fam, "")); got != "6.0.26100.8037" {
		t.Errorf("default value = %q, want %q", got, "6.0.26100.8037")
	}
	v := valueOf(t, fam, "6.0.26100.8037")
	if v.Type != regf.RegBinary || !bytes.Equal(v.Data, []byte{1}) {
		t.Errorf("per-version value type=%d data=%x, want REG_BINARY 01", v.Type, v.Data)
	}
}

func TestInstallWinnersValidation(t *testing.T) {
	software := newTestHiveRoot()
	if err := InstallWinners(software, []WinnersInstall{{KeyForm: "k", VersionFamily: "6.0"}}); err == nil {
		t.Error("accepted a WinnersInstall with no Version")
	}
	if err := InstallWinners(software, []WinnersInstall{
		{KeyForm: "k", VersionFamily: "6.0", Version: "5.82.26100.8037"},
	}); err == nil {
		t.Error("accepted a Version outside its VersionFamily")
	}
}
