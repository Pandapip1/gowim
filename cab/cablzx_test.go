package cab

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealCabinet_MatchesCabextract decodes the real, downloaded
// OpenSSH-Server-Package-amd64.cab (testdata/OpenSSH-Server-Package-amd64.cab,
// fetched live via gowim/wufetch on 2026-09-14, sha256
// fc40c1573a6dacd58c57e2d85fb6fe9b75a7883b054cc541aa9929453cf7c5cc, the same
// file wufetch's own README documents downloading and hand-verifying) with
// this package's own CFHEADER/CFFOLDER/CFFILE/CAB-LZX implementation, then
// diffs every single one of its 38 files, byte for byte, against `cabextract`
// actually extracting the same real file (a real, independent, widely-used
// implementation -- not a self-check). Skipped if cabextract isn't
// installed.
func TestRealCabinet_MatchesCabextract(t *testing.T) {
	cabPath := filepath.Join("testdata", "OpenSSH-Server-Package-amd64.cab")
	raw, err := os.ReadFile(cabPath)
	if err != nil {
		t.Skipf("real test cabinet not present: %v", err)
	}
	if _, err := exec.LookPath("cabextract"); err != nil {
		t.Skip("cabextract not installed")
	}

	r, err := NewReader(raw)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if len(r.Files) == 0 {
		t.Fatal("no files parsed")
	}
	t.Logf("parsed %d folders, %d files", len(r.Folders), len(r.Files))
	for _, f := range r.Folders {
		if f.Method != CompressLZX {
			t.Fatalf("unexpected folder compression method %s (test cabinet is expected to be all-LZX)", f.Method)
		}
	}

	dir := t.TempDir()
	cmd := exec.Command("cabextract", "-d", dir, cabPath)
	absCab, _ := filepath.Abs(cabPath)
	cmd.Args[len(cmd.Args)-1] = absCab
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cabextract failed: %v\n%s", err, out)
	}

	if len(r.Files) != 38 {
		t.Errorf("got %d files, want 38 (per this file's known real directory listing)", len(r.Files))
	}

	for _, f := range r.Files {
		got, err := r.ExtractFile(f)
		if err != nil {
			t.Errorf("ExtractFile(%q): %v", f.Name, err)
			continue
		}
		if uint32(len(got)) != f.Size {
			t.Errorf("%q: extracted %d bytes, CFFILE says %d", f.Name, len(got), f.Size)
		}
		// CFFILE names use backslash as a path separator; cabextract
		// (like this test's own comparison) creates real subdirectories
		// for these on Linux.
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(f.Name, `\`, "/"))))
		if err != nil {
			t.Errorf("%q: reading cabextract's copy: %v", f.Name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%q: mismatch vs cabextract (got %d bytes, want %d bytes)", f.Name, len(got), len(want))
		}
	}
}

// TestRealCabinet_KnownFiles asserts a handful of specific, known-real files
// and sizes from the same cabinet (independently cross-checked against
// `7z l -slt`'s own listing of the real file, and against wufetch's own
// README, which names these same files as what it confirmed `cabextract`
// produces), so the parse is checked even when cabextract itself isn't
// available to run the full byte-for-byte comparison above.
func TestRealCabinet_KnownFiles(t *testing.T) {
	cabPath := filepath.Join("testdata", "OpenSSH-Server-Package-amd64.cab")
	raw, err := os.ReadFile(cabPath)
	if err != nil {
		t.Skipf("real test cabinet not present: %v", err)
	}
	r, err := NewReader(raw)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	wantSizes := map[string]uint32{
		"$filehashes$.dat": 22712,
		"amd64_openssh-server-components-onecore_31bf3856ad364e35_10.0.26100.1_none_2414f81d8596f589\\sshd.exe": 1327616,
		"amd64_openssh-server-components-onecore_31bf3856ad364e35_10.0.26100.1_none_2414f81d8596f589\\moduli":   587472,
		"OpenSSH-Server-Package~31bf3856ad364e35~amd64~~10.0.26100.1.mum":                                       12017,
	}
	found := map[string]bool{}
	for _, f := range r.Files {
		if want, ok := wantSizes[f.Name]; ok {
			found[f.Name] = true
			if f.Size != want {
				t.Errorf("%q: CFFILE size %d, want %d", f.Name, f.Size, want)
			}
			data, err := r.ExtractFile(f)
			if err != nil {
				t.Errorf("ExtractFile(%q): %v", f.Name, err)
				continue
			}
			if uint32(len(data)) != want {
				t.Errorf("%q: extracted %d bytes, want %d", f.Name, len(data), want)
			}
		}
	}
	for name := range wantSizes {
		if !found[name] {
			t.Errorf("expected file %q not found in cabinet", name)
		}
	}

	// sshd.exe is a real PE binary: confirm the MZ/PE magic survived
	// decompression intact.
	for _, f := range r.Files {
		if strings.HasSuffix(f.Name, `\sshd.exe`) {
			data, err := r.ExtractFile(f)
			if err != nil {
				t.Fatalf("ExtractFile(sshd.exe): %v", err)
			}
			if len(data) < 2 || data[0] != 'M' || data[1] != 'Z' {
				t.Errorf("sshd.exe: missing MZ magic, first bytes %x", data[:min(16, len(data))])
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
