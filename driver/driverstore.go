package driver

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
)

// driverStoreHash computes the 64-bit rolling hash real Windows'
// `drvstore.dll` uses to name a staged driver package's DriverStore folder:
// a base-39 polynomial (Horner's method) accumulation over the driver's raw
// on-disk `.inf` file bytes -- no line-ending/encoding normalization, no
// involvement of architecture, payload files, or catalog content.
//
// Provenance: this scheme is genuinely undocumented anywhere publicly (a
// Microsoft engineer said as much on a 2008 public newsgroup thread,
// recommending SetupGetInfDriverStoreLocation() instead of computing it
// yourself) and was confirmed NOT a plain MD5/SHA-1/SHA-256 of the INF
// bytes. It was reverse-engineered (2026-07-13) via clean-room disassembly
// of the real `drvstore.dll` (a background agent using Capstone/pefile,
// reading only its machine code -- Microsoft ships no source for it, so
// this is ordinary black-box disassembly, not a licensing concern): the
// core loop lives at drvstore.dll RVA 0x20c2c (`h = h*0x27 + byte`,
// `imul rcx, rax, 0x27` / `add rax, rcx`), reached from a folder-name
// builder at RVA 0xb6328 that formats `"%ws_%ws_%ws"` (RVA 0x110ff8) from
// the INF's display name, an architecture string, and this hash's 8 bytes
// hex-encoded via a table at RVA 0x12cfe0. Empirically validated
// (2026-07-13) against 102 real driver packages sampled from a real
// Windows 11 23H2 image's `Windows\System32\DriverStore\FileRepository`:
// 102/102 exact matches. See TODO.md's "Driver package additions" entry for
// the full research trail, including the ruled-out
// `pGetDriverPackageHash`/`CryptCATAdminCalcHashFromFileHandle` red herring
// (a real, but unrelated, SHA-1-based catalog-thumbprint function) and the
// `ntprint.inf`/`prnms003.inf` corpus anomaly that helped confirm the hash
// depends only on the INF's own bytes (identical INF bytes across the
// amd64/x86 variants of a shared multi-platform INF produce the identical
// hash, despite different payload sets and different catalog files).
func driverStoreHash(data []byte) uint64 {
	var h uint64
	for _, b := range data {
		h = h*39 + uint64(b)
	}
	return h
}

// FileRepositoryDirName returns the DriverStore folder name real Windows
// would stage a driver package under -- `<infName>_<platform>_<hash>`,
// where hash is driverStoreHash(infData)'s 8 bytes, hex-encoded in
// little-endian (memory) order, e.g. "1394.inf_amd64_f05cd2933ff9e649". See
// driverStoreHash's doc comment for the algorithm's provenance/validation.
// infName is used verbatim (including its ".inf" extension and original
// case) -- real sampled folder names never renamed the INF's own basename.
func FileRepositoryDirName(infName string, infData []byte, platform string) string {
	h := driverStoreHash(infData)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], h)
	return infName + "_" + platform + "_" + hex.EncodeToString(buf[:])
}

// fileRepositoryDirName reads pkg's own INF file and returns the
// DriverStore folder name real Windows would stage it under (see
// FileRepositoryDirName), for use as Install's automatic DIRID 13
// destination when the caller does not supply one explicitly.
func (pkg *Package) fileRepositoryDirName() (string, error) {
	infPath := path.Join(pkg.Dir, pkg.INFName)
	data, err := fs.ReadFile(pkg.FSys, infPath)
	if err != nil {
		return "", fmt.Errorf("read INF %s for DriverStore folder name: %w", infPath, err)
	}
	return FileRepositoryDirName(pkg.INFName, data, pkg.Platform), nil
}
