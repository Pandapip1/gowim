package wufetch

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealDownload_OpenSSHServer26200Amd64 is this package's real proof:
// it locates and downloads the actual OpenSSH.Server~~~~0.0.1.0 capability
// payload for Windows 11, version 25H2 (build 26200), amd64, from
// Microsoft's real update CDN, over the real network, and then validates
// the result as a real, non-corrupt MS-CAB file whose contents look like
// OpenSSH.Server (see cabValidate below).
//
// Skipped unless GOWIM_TEST_NETWORK=1, so `go test ./...` from the
// workspace root never requires network access. Run explicitly with:
//
//	GOWIM_TEST_NETWORK=1 go test -run TestRealDownload -v ./...
func TestRealDownload_OpenSSHServer26200Amd64(t *testing.T) {
	if os.Getenv("GOWIM_TEST_NETWORK") != "1" {
		t.Skip("set GOWIM_TEST_NETWORK=1 to run this test (makes real HTTPS requests to api.uupdump.net and Microsoft's update CDN)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	c := &Client{}

	build, err := c.FindBuild(ctx, "26200", "amd64")
	if err != nil {
		t.Fatalf("FindBuild: %v", err)
	}
	t.Logf("resolved build: %+v", build)

	var buf bytes.Buffer
	f, err := c.DownloadCapability(ctx, build, "OpenSSH.Server~~~~0.0.1.0", "amd64", "", &buf)
	if err != nil {
		t.Fatalf("DownloadCapability: %v", err)
	}
	t.Logf("downloaded %s: %d bytes, sha256=%s", f.Name, buf.Len(), f.SHA256)

	if buf.Len() < 1_000_000 {
		t.Fatalf("downloaded file suspiciously small: %d bytes", buf.Len())
	}

	entries, err := cabFileNames(buf.Bytes())
	if err != nil {
		t.Fatalf("parsing downloaded file as MS-CAB: %v", err)
	}
	t.Logf("cab contains %d entries", len(entries))

	var sawSshd, sawKeygen, sawMum bool
	for _, name := range entries {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "sshd.exe") {
			sawSshd = true
		}
		if strings.Contains(lower, "ssh-keygen.exe") {
			sawKeygen = true
		}
		if strings.HasSuffix(lower, ".mum") {
			sawMum = true
		}
	}
	if !sawSshd {
		t.Error("cab file list does not contain sshd.exe")
	}
	if !sawKeygen {
		t.Error("cab file list does not contain ssh-keygen.exe")
	}
	if !sawMum {
		t.Error("cab file list does not contain a .mum servicing manifest")
	}
}

// cabFileNames parses just enough of the MS-CAB container format --
// CFHEADER then each CFFILE entry's name -- to enumerate a cabinet's file
// list without decompressing anything, per the documented format at
// https://learn.microsoft.com/en-us/previous-versions/bb417343(v=msdn.10)
// ("Microsoft Cabinet File Format"). This is deliberately minimal (no
// CFFOLDER/CFDATA decompression) -- it exists only so this package's real
// network test can independently sanity-check the downloaded file's
// structure and contents without depending on an external `cabextract`
// binary or a new full CAB-parsing package elsewhere in this repo. Layout
// confirmed field-by-field against this test's own real downloaded file
// with `xxd`, 2026-09-14 -- see README.md.
func cabFileNames(data []byte) ([]string, error) {
	if len(data) < 36 || string(data[0:4]) != "MSCF" {
		return nil, fmt.Errorf("missing MSCF signature")
	}

	// cbCabinet is the cabinet's own declared size. Real files downloaded
	// from Microsoft's update CDN have been observed to carry trailing
	// padding beyond cbCabinet (confirmed empirically, 2026-09-14: a real
	// downloaded OpenSSH-Server-Package-amd64.cab is 9720 bytes larger than
	// its own cbCabinet -- `cabextract` warns about exactly this ("possible
	// N extra bytes at end of file") but still extracts successfully), so
	// only reject a file *smaller* than its declared size, not a larger one.
	cbCabinet := binary.LittleEndian.Uint32(data[8:12])
	if int(cbCabinet) > len(data) {
		return nil, fmt.Errorf("cbCabinet %d exceeds actual file size %d", cbCabinet, len(data))
	}

	coffFiles := binary.LittleEndian.Uint32(data[16:20])
	cFiles := binary.LittleEndian.Uint16(data[28:30])
	flags := binary.LittleEndian.Uint16(data[30:32])

	off := 36
	const cfhdrReservePresent = 0x0004
	if flags&cfhdrReservePresent != 0 {
		if len(data) < off+4 {
			return nil, fmt.Errorf("truncated reserve-size fields")
		}
		cbCFHeader := binary.LittleEndian.Uint16(data[off : off+2])
		off += 4 // cbCFHeader(2) + cbCFFolder(1) + cbCFData(1)
		off += int(cbCFHeader)
	}
	const cfhdrHasPrev, cfhdrHasNext = 0x0001, 0x0002
	if flags&cfhdrHasPrev != 0 {
		off = skipCString(data, off)
		off = skipCString(data, off) // szDiskPrev, szCabinetPrev
	}
	if flags&cfhdrHasNext != 0 {
		off = skipCString(data, off)
		off = skipCString(data, off) // szDiskNext, szCabinetNext
	}
	_ = off // header parse above is for completeness; CFFILE offset is authoritative

	pos := int(coffFiles)
	names := make([]string, 0, cFiles)
	for i := 0; i < int(cFiles); i++ {
		if pos+16 > len(data) {
			return nil, fmt.Errorf("truncated CFFILE entry %d", i)
		}
		// cbFile(4) uoffFolderStart(4) iFolder(2) date(2) time(2) attribs(2) name(variable, NUL-terminated)
		nameStart := pos + 16
		end := bytes.IndexByte(data[nameStart:], 0)
		if end < 0 {
			return nil, fmt.Errorf("unterminated file name in CFFILE entry %d", i)
		}
		names = append(names, string(data[nameStart:nameStart+end]))
		pos = nameStart + end + 1
	}
	return names, nil
}

func skipCString(data []byte, off int) int {
	if off >= len(data) {
		return off
	}
	end := bytes.IndexByte(data[off:], 0)
	if end < 0 {
		return len(data)
	}
	return off + end + 1
}
