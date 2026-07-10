package driver

import "strings"

// CriticalDeviceDatabaseEntry is one hardware/compatible ID registration
// this package would write under
// HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Control\CriticalDeviceDatabase,
// the documented mechanism for registering a device's hardware ID against a
// service before the device is ever seen by PnP - see the "Critical Device
// Database" description in the archived "Critical Device Database TIP"
// article,
// https://learn.microsoft.com/archive/blogs/ntdebugging/critical-device-database-tip
// ("This database stores configuration data for new devices that must be
// installed and started before the Windows components that normally
// install devices have been started... the hardware ID is written to the
// CDDB in the registry, so that if the device is determined to be new, it
// can be found there during boot"), which also gives the registry path
// (HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Control\CriticalDeviceDatabase)
// and a worked example subkey name
// (pci#ven_8086&dev_244e, i.e. lowercased with '\' replaced by '#') for the
// hardware ID "PCI\VEN_8086&DEV_244E". The same '\' -> '#' registry-subkey
// escaping convention (required because '\' is not a valid character within
// a single registry subkey name component) is independently documented for
// the unrelated but analogous DeviceOverrides\HardwareID subkey - see
// "HardwareID Registry Subkey",
// https://learn.microsoft.com/windows-hardware/drivers/install/hardwareid-registry-subkey
// ("you must replace it with the number character... you must create a
// HardwareID registry subkey with a name of USB#VID_1234&PID_ABCD&REV_0001").
// The ClassGUID/Service value names under each CriticalDeviceDatabase
// subkey are not spelled out on a single Microsoft Learn reference page, but
// are consistently corroborated by real-world examples (e.g. a Windows
// primary IDE channel's CriticalDeviceDatabase entry recorded as
// "ClassGUID"="{4D36E96A-E325-11CE-BFC1-08002BE10318}" and
// "Service"="atapi") and match this package's own [Version] ClassGuid /
// AddService-associated-service model; no other commonly-required value
// name is documented, so none beyond these two is written.
type CriticalDeviceDatabaseEntry struct {
	// HardwareID is the raw hardware or compatible ID string from a Models
	// section entry (e.g. `ACPI\CONTOSO0001`), before the subkey-naming
	// transform (see CriticalDeviceDatabaseSubkeyName).
	HardwareID string
	// ClassGuid is the INF's [Version] section ClassGuid field, formatted
	// as "{nnnnnnnn-nnnn-nnnn-nnnn-nnnnnnnnnnnn}", or "" if absent.
	ClassGuid string
	// Service is the name of the Models entry's install section's
	// associated service (the AddService directive whose flags have the
	// SPSVCINST_ASSOCSERVICE bit set - see ServiceInstall.AssocService), or
	// "" if that install section has no associated service.
	Service string
	// InstallSection is the undecorated install-section-name the hardware
	// ID was reached through, for diagnostics.
	InstallSection string
}

// CriticalDeviceDatabaseSubkeyName applies the documented CriticalDeviceDatabase
// subkey-naming transform to a hardware or compatible ID: lowercase, with
// every '\' replaced by '#' (see the CriticalDeviceDatabaseEntry doc comment
// for citations).
func CriticalDeviceDatabaseSubkeyName(hardwareID string) string {
	return strings.ToLower(strings.ReplaceAll(hardwareID, `\`, "#"))
}

// CriticalDeviceDatabaseEntries enumerates one CriticalDeviceDatabaseEntry
// per distinct hardware/compatible ID named in pkg's [Manufacturer] ->
// Models section entries (see enumerateModelSections), deduplicated by
// HardwareID (case-insensitive, keeping the first occurrence's ClassGuid/
// Service/InstallSection). Each entry's Service is the associated service
// (if any) of the specific install section that hardware ID was reached
// through, per Services(platform).
func (p *Package) CriticalDeviceDatabaseEntries(platform string) ([]CriticalDeviceDatabaseEntry, error) {
	services, err := p.Services(platform)
	if err != nil {
		return nil, err
	}
	assocBySection := make(map[string]string, len(services))
	for _, s := range services {
		if s.AssocService {
			// "Every device driver INF should have exactly one associated
			// service" per install section; first one found wins.
			if _, ok := assocBySection[s.InstallSection]; !ok {
				assocBySection[s.InstallSection] = s.Name
			}
		}
	}

	classGuid := p.INF.Version().ClassGuid

	var out []CriticalDeviceDatabaseEntry
	seen := make(map[string]bool)
	for _, ms := range enumerateModelSections(p.INF, platform) {
		for _, hwid := range ms.HardwareIDs {
			hwid = strings.TrimSpace(hwid)
			if hwid == "" {
				continue
			}
			key := strings.ToLower(hwid)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, CriticalDeviceDatabaseEntry{
				HardwareID:     hwid,
				ClassGuid:      classGuid,
				Service:        assocBySection[ms.InstallSection],
				InstallSection: ms.InstallSection,
			})
		}
	}
	return out, nil
}
