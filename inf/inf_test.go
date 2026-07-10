package inf

import (
	"strings"
	"testing"
)

// sampleINF is a minimal, but realistic, driver INF exercising [Version],
// [Manufacturer], [Models] (via a manufacturer-referenced section),
// [SourceDisksFiles], [DestinationDirs], and [Strings].
const sampleINF = `; Contoso example widget driver
[Version]
Signature   =   "$Windows NT$"
Class       = Net
ClassGuid   = {4d36e972-e325-11ce-bfc1-08002be10318}
Provider    = %Contoso%
CatalogFile = contoso.cat
DriverVer   = 01/29/2010,1.2.3.4

[Manufacturer]
%Contoso% = ContosoModels,NTamd64

[ContosoModels.NTamd64]
%Contoso.DeviceDesc% = Contoso_Install, PCI\VEN_10EE&DEV_0001

[Contoso_Install]
CopyFiles = Contoso_Files

[Contoso_Files]
contoso.sys

[SourceDisksNames]
1 = %DiskName%

[SourceDisksFiles]
contoso.sys = 1

[DestinationDirs]
DefaultDestDir = 12
Contoso_Files  = 12

[Strings]
Contoso           = "Contoso"
Contoso.DeviceDesc = "Contoso Network Adapter"
DiskName           = "Contoso Driver Disk"
`

func TestParseFileBasicRoundTrip(t *testing.T) {
	f, err := ParseFile([]byte(sampleINF))
	if err != nil {
		t.Fatal(err)
	}
	if f.Unicode {
		t.Fatal("expected non-Unicode detection for plain ASCII input")
	}

	ver := f.Version()
	want := VersionInfo{
		Signature:   "$Windows NT$",
		Class:       "Net",
		ClassGuid:   "{4d36e972-e325-11ce-bfc1-08002be10318}",
		Provider:    "%Contoso%",
		DriverVer:   "01/29/2010,1.2.3.4",
		CatalogFile: "contoso.cat",
	}
	if ver != want {
		t.Fatalf("Version() = %+v, want %+v", ver, want)
	}

	if got, ok := f.Lookup("Contoso", ""); !ok || got != "Contoso" {
		t.Fatalf("Lookup(Contoso) = %q, %v", got, ok)
	}
	if got := f.Expand(ver.Provider, ""); got != "Contoso" {
		t.Fatalf("Expand(%%Contoso%%) = %q, want Contoso", got)
	}

	out := f.AppendTo(nil)
	f2, err := ParseFile(out)
	if err != nil {
		t.Fatalf("re-parse of serialized output failed: %v", err)
	}
	if ver2 := f2.Version(); ver2 != want {
		t.Fatalf("round-trip Version() = %+v, want %+v", ver2, want)
	}

	// Re-serializing the already-canonical output must be a fixed point.
	out2 := f2.AppendTo(nil)
	if string(out) != string(out2) {
		t.Fatalf("re-serialization is not a fixed point:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}

	// Section order and duplicate/merged structure.
	names := make([]string, len(f.Sections))
	for i, s := range f.Sections {
		names[i] = s.Name
	}
	wantNames := []string{
		"", "Version", "Manufacturer", "ContosoModels.NTamd64", "Contoso_Install",
		"Contoso_Files", "SourceDisksNames", "SourceDisksFiles", "DestinationDirs", "Strings",
	}
	if len(names) != len(wantNames) {
		t.Fatalf("section names = %v, want %v", names, wantNames)
	}
	for i := range names {
		if names[i] != wantNames[i] {
			t.Fatalf("section[%d] = %q, want %q", i, names[i], wantNames[i])
		}
	}

	// The preamble comment ("; Contoso example widget driver") is
	// preserved as a comment-only Entry ahead of any [Section] header.
	if f.Sections[0].Name != "" {
		t.Fatalf("expected an unnamed preamble section, got %q first", f.Sections[0].Name)
	}
	if len(f.Sections[0].Entries) != 1 || !f.Sections[0].Entries[0].CommentOnly {
		t.Fatalf("preamble entries = %+v, want one CommentOnly entry", f.Sections[0].Entries)
	}
	if got := f.Sections[0].Entries[0].Comment; got != "Contoso example widget driver" {
		t.Fatalf("preamble comment = %q", got)
	}

	// [Contoso_Files] has one bare (keyless) directive entry.
	cf, ok := f.Section("Contoso_Files")
	if !ok {
		t.Fatal("missing Contoso_Files section")
	}
	if len(cf.Entries) != 2 || cf.Entries[0].HasKey || !cf.Entries[1].Blank {
		t.Fatalf("Contoso_Files entries = %+v, want one keyless entry followed by the trailing blank line", cf.Entries)
	}
	if got, ok := cf.Entries[0].Field(0); !ok || got != "contoso.sys" {
		t.Fatalf("Contoso_Files field = %q, %v", got, ok)
	}

	// [DestinationDirs] has two entries sharing a section, in order.
	dd, ok := f.Section("DestinationDirs")
	if !ok {
		t.Fatal("missing DestinationDirs section")
	}
	if len(dd.Entries) != 3 || dd.Entries[0].Key != "DefaultDestDir" || dd.Entries[1].Key != "Contoso_Files" || !dd.Entries[2].Blank {
		t.Fatalf("DestinationDirs entries = %+v", dd.Entries)
	}
}

func TestParseFileComments(t *testing.T) {
	const src = `[Version]
Signature = "$Windows NT$"   ; required
; a standalone remark
Class = Net

; trailing banner
`
	f, err := ParseFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := f.Section("Version")
	if !ok {
		t.Fatal("missing Version section")
	}
	if len(sec.Entries) != 5 {
		t.Fatalf("entries = %+v, want 5 (Signature, standalone comment, Class, blank, trailing banner)", sec.Entries)
	}
	if sec.Entries[0].Comment != "required" {
		t.Fatalf("Signature entry comment = %q, want %q", sec.Entries[0].Comment, "required")
	}
	if !sec.Entries[1].CommentOnly || sec.Entries[1].Comment != "a standalone remark" {
		t.Fatalf("entry[1] = %+v, want standalone comment", sec.Entries[1])
	}
	if sec.Entries[2].Key != "Class" {
		t.Fatalf("entry[2] = %+v, want Class entry", sec.Entries[2])
	}
	if !sec.Entries[3].Blank {
		t.Fatalf("entry[3] = %+v, want blank line", sec.Entries[3])
	}
	if !sec.Entries[4].CommentOnly || sec.Entries[4].Comment != "trailing banner" {
		t.Fatalf("entry[4] = %+v, want trailing banner comment", sec.Entries[4])
	}

	out := string(f.AppendTo(nil))
	if !strings.Contains(out, "Signature = $Windows NT$  ; required") {
		t.Fatalf("serialized output missing canonical comment placement:\n%s", out)
	}
	if !strings.Contains(out, "; a standalone remark") {
		t.Fatalf("serialized output missing standalone comment:\n%s", out)
	}
}

func TestParseFileLineContinuation(t *testing.T) {
	const src = "[Contoso_Install]\r\n" +
		"CopyFiles = " + `"SomeDirectory\"` + "\\\r\n" +
		",SomeFile\r\n"
	f, err := ParseFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := f.Section("Contoso_Install")
	if !ok {
		t.Fatal("missing Contoso_Install section")
	}
	if len(sec.Entries) != 1 {
		t.Fatalf("entries = %+v, want exactly one joined entry", sec.Entries)
	}
	e := sec.Entries[0]
	if !e.HasKey || e.Key != "CopyFiles" {
		t.Fatalf("entry = %+v, want key CopyFiles", e)
	}
	if len(e.Fields) != 2 || e.Fields[0] != `SomeDirectory\` || e.Fields[1] != "SomeFile" {
		t.Fatalf("fields = %#v, want [SomeDirectory\\, SomeFile]", e.Fields)
	}

	out := string(f.AppendTo(nil))
	if !strings.Contains(out, `CopyFiles = "SomeDirectory\", SomeFile`) {
		t.Fatalf("serialized output = %q, want a single joined, re-quoted line", out)
	}
}

func TestParseFileQuotedFields(t *testing.T) {
	const src = `[add-registry-section]
HKR,,EventMessageFile,0x00020000,"%%SystemRoot%%\System32\IoLogMsg.dll"
HKR,,Example,,"Display an ""example"" string"
`
	f, err := ParseFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := f.Section("add-registry-section")
	if !ok {
		t.Fatal("missing add-registry-section")
	}
	if len(sec.Entries) != 2 {
		t.Fatalf("entries = %+v, want 2", sec.Entries)
	}
	e0 := sec.Entries[0]
	if e0.HasKey {
		t.Fatalf("entry 0 should be keyless (bare directive), got HasKey with Key=%q", e0.Key)
	}
	if len(e0.Fields) != 5 || e0.Fields[4] != `%%SystemRoot%%\System32\IoLogMsg.dll` {
		t.Fatalf("entry 0 fields = %#v", e0.Fields)
	}
	e1 := sec.Entries[1]
	if len(e1.Fields) != 5 || e1.Fields[4] != `Display an "example" string` {
		t.Fatalf("entry 1 fields = %#v, want unescaped doubled quotes", e1.Fields)
	}

	out := string(f.AppendTo(nil))
	if !strings.Contains(out, `"Display an ""example"" string"`) {
		t.Fatalf("serialized output did not re-escape the quote:\n%s", out)
	}

	f2, err := ParseFile([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	sec2, _ := f2.Section("add-registry-section")
	if sec2.Entries[1].Fields[4] != e1.Fields[4] {
		t.Fatalf("round-tripped quoted field = %q, want %q", sec2.Entries[1].Fields[4], e1.Fields[4])
	}
}

func TestParseFileUnicodeBOM(t *testing.T) {
	orig, err := ParseFile([]byte(sampleINF))
	if err != nil {
		t.Fatal(err)
	}
	orig.Unicode = true
	encoded := orig.AppendTo(nil)

	if len(encoded) < 2 || encoded[0] != 0xff || encoded[1] != 0xfe {
		t.Fatalf("encoded output missing UTF-16LE BOM: % x", encoded[:8])
	}

	f, err := ParseFile(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Unicode {
		t.Fatal("expected Unicode detection from BOM")
	}
	if got := f.Version(); got != orig.Version() {
		t.Fatalf("Version() after Unicode round trip = %+v, want %+v", got, orig.Version())
	}
	if got, ok := f.Lookup("Contoso.DeviceDesc", ""); !ok || got != "Contoso Network Adapter" {
		t.Fatalf("Lookup(Contoso.DeviceDesc) = %q, %v", got, ok)
	}

	// Re-encoding must reproduce the same bytes (fixed point through the
	// Unicode path too).
	out2 := f.AppendTo(nil)
	if string(encoded) != string(out2) {
		t.Fatal("Unicode round trip is not byte-exact")
	}
}

func TestParseFileEmptyAndMissingVersion(t *testing.T) {
	f, err := ParseFile([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sections) != 0 {
		t.Fatalf("Sections = %+v, want none for empty input", f.Sections)
	}
	if v := f.Version(); v != (VersionInfo{}) {
		t.Fatalf("Version() on empty file = %+v, want zero value", v)
	}
	if got, ok := f.Lookup("Missing", ""); ok || got != "" {
		t.Fatalf("Lookup on missing key = %q, %v, want \"\", false", got, ok)
	}
}

func TestCatalogFileForPlatform(t *testing.T) {
	const src = `[Version]
Signature = "$Windows NT$"
CatalogFile = generic.cat
CatalogFile.ntamd64 = amd64.cat
`
	f, err := ParseFile([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := f.CatalogFileForPlatform("ntamd64"); !ok || got != "amd64.cat" {
		t.Fatalf("CatalogFileForPlatform(ntamd64) = %q, %v", got, ok)
	}
	if got, ok := f.CatalogFileForPlatform("ntarm64"); !ok || got != "generic.cat" {
		t.Fatalf("CatalogFileForPlatform(ntarm64) = %q, %v, want fallback to generic.cat", got, ok)
	}
}
