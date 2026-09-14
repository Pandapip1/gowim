package component

import (
	"bytes"
	"embed"
	"testing"

	"github.com/Pandapip1/gowim/wim"
)

// TestInstall_RealOpenSSHCapability is this package's answer to its own
// README's long-standing "No live confirmation" gap (see README's "Known
// gaps" section): it installs the real, genuine `OpenSSH.Server` Windows
// Capability payload -- not a synthetic fixture -- and checks the resulting
// directory tree byte-for-byte against the real files.
//
// Provenance of testdata/openssh/: extracted from the real
// `OpenSSH-Server-Package-amd64.cab` (sha256
// fc40c1573a6dacd58c57e2d85fb6fe9b75a7883b054cc541aa9929453cf7c5cc) that the
// sibling `wufetch` package downloaded live from Microsoft's own update CDN
// on 2026-09-14 (see wufetch's README and its
// TestRealDownload_OpenSSHServer26200Amd64), via this repo's new sibling
// `cab` package (itself verified byte-for-byte against `cabextract`'s
// independent extraction of the same file -- see cab's README). Each
// `.manifest` file was additionally decompressed through the sibling `pa30`
// package's DecodeWithSource (using pa30's own real
// testdata/wcp_dictionary.bin) before being committed here, since Install
// requires plain-XML manifests and the real files are PA30-compressed. Two
// of the capability's real components are included --
// OpenSSH-Server-Components-Onecore (sshd.exe, moduli, sftp-server.exe,
// ssh-shellhost.exe, sshd_config_default) and OpenSSH-Common-Components-
// Onecore (ssh-keygen.exe, ssh-agent.exe, scp.exe, ssh-add.exe) -- omitting
// only that second component's LICENSE.txt/NOTICE.txt (not needed to prove
// the install path, and sizable) and the capability's third-party-signed
// `.cat` catalogs (Install's Catalog field is optional; see PackageInstall's
// doc comment on why a third-party catalog can never chain to a Microsoft
// root regardless).
//
//go:embed testdata/openssh
var opensshFixtures embed.FS

// opensshKeyForm is a synthetic-but-well-formed WinSxS keyform for this
// test's installed components. It is NOT the real CBS identity hash --
// gowim cannot compute that (see ComponentInstall.KeyForm's doc comment) --
// it is only shaped like one (16 hex digits) so Install's own validation
// (payload file name length, keyform-derived paths) exercises the real code
// paths. This is exactly the BuildOnce use case the README's own
// build-once-vs-serviceable table describes: a throwaway, never-updated
// image (this test doesn't call InstallRegistry at all, matching that
// choice -- see the standalone validation harness this test's package-level
// comment points at for the reasoning behind picking BuildOnce for the
// actual Packer pipeline).
const opensshServerKeyForm = "amd64_openssh-server-components-onecore_deadbeefcafef00d"
const opensshCommonKeyForm = "amd64_openssh-common-components-onecore_cafebabedeadbeef"

func mustReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := opensshFixtures.ReadFile("testdata/openssh/" + path)
	if err != nil {
		t.Fatalf("reading real fixture %s: %v", path, err)
	}
	return data
}

func TestInstall_RealOpenSSHCapability(t *testing.T) {
	serverManifest := mustReadFixture(t, "server_component/component.manifest")
	commonManifest := mustReadFixture(t, "common_component/component.manifest")
	sshdExe := mustReadFixture(t, "server_component/sshd.exe")
	moduli := mustReadFixture(t, "server_component/moduli")
	sftpServer := mustReadFixture(t, "server_component/sftp-server.exe")
	sshShellhost := mustReadFixture(t, "server_component/ssh-shellhost.exe")
	sshdConfig := mustReadFixture(t, "server_component/sshd_config_default")
	sshKeygen := mustReadFixture(t, "common_component/ssh-keygen.exe")
	sshAgent := mustReadFixture(t, "common_component/ssh-agent.exe")
	scp := mustReadFixture(t, "common_component/scp.exe")
	sshAdd := mustReadFixture(t, "common_component/ssh-add.exe")
	packageMUM := mustReadFixture(t, "OpenSSH-Server-Package.mum")

	// Sanity check the fixtures really are what they claim before using
	// them: real PE binaries (MZ magic) and a real plain-XML manifest/mum
	// (not PA30/DCM-prefixed -- Install rejects those).
	for name, data := range map[string][]byte{"sshd.exe": sshdExe, "ssh-keygen.exe": sshKeygen} {
		if len(data) < 2 || data[0] != 'M' || data[1] != 'Z' {
			t.Fatalf("fixture %s missing MZ magic", name)
		}
	}
	for name, data := range map[string][]byte{"server manifest": serverManifest, "common manifest": commonManifest, "package mum": packageMUM} {
		if bytes.HasPrefix(data, []byte("DC")) {
			t.Fatalf("fixture %s is still PA30-compressed, not plain XML", name)
		}
		if !bytes.Contains(data, []byte("<?xml")) {
			t.Fatalf("fixture %s does not look like XML", name)
		}
	}

	root, bt := newTestImage()

	inst := &Installation{
		Serviceability: BuildOnce,
		Components: []ComponentInstall{
			{
				KeyForm:  opensshServerKeyForm,
				Manifest: serverManifest,
				Files: []PayloadFile{
					{Name: "sshd.exe", Data: sshdExe, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "moduli", Data: moduli, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "sftp-server.exe", Data: sftpServer, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "ssh-shellhost.exe", Data: sshShellhost, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "sshd_config_default", Data: sshdConfig, DestDirs: []string{`Windows\System32\OpenSSH`}},
				},
			},
			{
				KeyForm:  opensshCommonKeyForm,
				Manifest: commonManifest,
				Files: []PayloadFile{
					{Name: "ssh-keygen.exe", Data: sshKeygen, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "ssh-agent.exe", Data: sshAgent, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "scp.exe", Data: scp, DestDirs: []string{`Windows\System32\OpenSSH`}},
					{Name: "ssh-add.exe", Data: sshAdd, DestDirs: []string{`Windows\System32\OpenSSH`}},
				},
			},
		},
		Packages: []PackageInstall{
			{
				Name: "OpenSSH-Server-Package~31bf3856ad364e35~amd64~~10.0.26100.1",
				MUM:  packageMUM,
			},
		},
	}

	newRoot, newBlobs, err := Install(&wim.ImageMetadata{Root: root.Root}, bt, inst)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Every real payload file must be reachable both from its WinSxS store
	// copy and from its System32 projection, with byte-identical content --
	// this is the whole point of the component model (two directory
	// entries, one blob).
	checks := []struct {
		path string
		want []byte
	}{
		{`Windows\WinSxS\Manifests\` + opensshServerKeyForm + `.manifest`, serverManifest},
		{`Windows\WinSxS\Manifests\` + opensshCommonKeyForm + `.manifest`, commonManifest},
		{`Windows\WinSxS\` + opensshServerKeyForm + `\sshd.exe`, sshdExe},
		{`Windows\System32\OpenSSH\sshd.exe`, sshdExe},
		{`Windows\System32\OpenSSH\ssh-keygen.exe`, sshKeygen},
		{`Windows\System32\OpenSSH\moduli`, moduli},
		{`Windows\servicing\Packages\OpenSSH-Server-Package~31bf3856ad364e35~amd64~~10.0.26100.1.mum`, packageMUM},
	}

	blobData := make(map[wim.Hash][]byte, len(newBlobs))
	for _, b := range newBlobs {
		blobData[b.Hash] = b.Data
	}

	for _, c := range checks {
		e, err := newRoot.Lookup(c.path)
		if err != nil {
			t.Errorf("Lookup(%q): %v", c.path, err)
			continue
		}
		data, ok := blobData[e.Streams[0].Hash]
		if !ok {
			t.Errorf("%q: blob hash %x not among Install's returned newBlobs", c.path, e.Streams[0].Hash)
			continue
		}
		if !bytes.Equal(data, c.want) {
			t.Errorf("%q: content mismatch (got %d bytes, want %d bytes)", c.path, len(data), len(c.want))
		}
	}

	// sshd.exe's WinSxS store copy and its System32 projection must be the
	// *same* blob (one file on disk, two directory entries) -- exactly what
	// makes Install's real-image "component store copy and destination copy
	// are the same content reached through two hardlinks" claim (see
	// PayloadFile's doc comment) hold for this real file.
	storeEntry, err := newRoot.Lookup(`Windows\WinSxS\` + opensshServerKeyForm + `\sshd.exe`)
	if err != nil {
		t.Fatalf("Lookup WinSxS sshd.exe: %v", err)
	}
	sysEntry, err := newRoot.Lookup(`Windows\System32\OpenSSH\sshd.exe`)
	if err != nil {
		t.Fatalf("Lookup System32 sshd.exe: %v", err)
	}
	if storeEntry.Streams[0].Hash != sysEntry.Streams[0].Hash {
		t.Errorf("sshd.exe: WinSxS store copy and System32 projection are different blobs (hash %x vs %x)", storeEntry.Streams[0].Hash, sysEntry.Streams[0].Hash)
	}

	// BuildOnce means InstallRegistry must refuse to run -- checked here so
	// this test also stands as the documented reminder of exactly what a
	// BuildOnce install of this real capability does *not* get you (no
	// COMPONENTS/SOFTWARE hive bookkeeping, and therefore -- unverified by
	// this package, since nothing here touches a running Windows or DISM --
	// almost certainly no `Get-WindowsCapability`-reported "Installed"
	// state, even though the binaries are genuinely in place and runnable).
	if err := InstallRegistry(&Hives{}, inst); err != ErrBuildOnce {
		t.Errorf("InstallRegistry on a BuildOnce installation: got %v, want ErrBuildOnce", err)
	}

	t.Logf("installed %d real OpenSSH.Server payload files across %d components + 1 package, %d new blobs",
		5+4, len(inst.Components), len(newBlobs))
}
