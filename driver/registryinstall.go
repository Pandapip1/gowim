// This file extends the driver package to write a driver package's Services
// and CriticalDeviceDatabase registry registration into a SYSTEM-hive-shaped
// *regf.Key tree, using the sibling regf package
// (github.com/Pandapip1/gowim/regf) purely as a plain Key/Value struct tree
// - exactly as install.go builds *wim.DirEntry trees by hand with no special
// "tree API" from the wim package. The generic *regf.Key/*regf.Value
// find-or-create/navigation logic itself (and the AddService-derived
// service-registration schema) now lives in the sibling
// github.com/Pandapip1/gowim/service package (see its README), which this
// file's Services-related code delegates to - service.FindSubkey/
// FindOrCreateSubkey/FindValue/SetValue for CriticalDeviceDatabase's own
// navigation needs below, and service.Install (via a service.Service built
// from a resolved ServiceInstall) for Services\<name>.
//
// Explicit non-goals (in addition to those in driver.go's package doc):
//
//   - The DriverDatabase key tree (SYSTEM\DriverDatabase\DriverPackages,
//     DeviceIds, etc.), the internal driver-ranking database PnP uses to
//     choose among multiple matching drivers for a device. This was checked,
//     not just assumed: a Microsoft Learn documentation search turned up no
//     authoritative page describing DriverDatabase's subkey/value schema
//     (unlike CriticalDeviceDatabase and the Services key schema, both of
//     which are documented - see criticaldevicedatabase.go and the sibling
//     service package's citations); the only discussion found was community
//     speculation (e.g. a Microsoft Q&A/TechNet thread describing it as
//     "redundantly duplicat[ing]" FileRepository metadata, with no cited
//     schema). Reproducing it would mean reverse-engineering undocumented
//     internals, which is out of scope here - the same policy driver.go's
//     DriverStore-hash non-goal states.
//   - The Enum device-instance tree
//     (SYSTEM\CurrentControlSet\Enum\<enumerator>\<device-id>\<instance-id>).
//     Registering an actual device instance there requires a real, live
//     hardware instance ID discovered by PnP enumeration at boot/setup time,
//     which an offline image-prep tool run against a driver package alone
//     does not have.
//   - INFCACHE.1 (the binary cache of parsed INF metadata SetupAPI maintains
//     under %SystemRoot%\INF): a distinct, undocumented binary format in its
//     own right.
//   - AddReg/DelReg directives in general, beyond the specific Services
//     (Type/Start/ErrorControl/ImagePath/Group/DependOnGroup/
//     DependOnService) and CriticalDeviceDatabase (ClassGUID/Service) values
//     this file writes: a generic, much broader directive mechanism this
//     package does not take on.
//   - Registry-hive writing back to disk (deciding which hive file to open,
//     backing it up, replacing it): InstallRegistry only produces/merges
//     regf.Key/regf.Value structures given an already-loaded *regf.Hive's (or
//     freshly-built) Key tree; the caller handles file I/O, exactly as
//     Install's wim-side counterpart only returns in-memory nodes rather than
//     writing a WIM file itself.
package driver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/service"
)

// mergeServiceInstall resolves svc's BinaryDirID+BinaryPath (via destDirs)
// into a final ImagePath string using the same pathComponents helper Install
// (install.go) uses for payload files, builds a service.Service from the
// resolved fields, and delegates the actual Services\<name> subkey
// creation/merge to service.Install - see that function's doc comment for
// the citations behind the registry schema/value names/types it writes.
//
// destDirs supplies the destination directory path for svc.BinaryDirID, in
// the same shape Install (install.go) already takes for payload files, so a
// single destDirs map serves both file placement and ImagePath computation.
func mergeServiceInstall(servicesKey *regf.Key, svc ServiceInstall, destDirs map[DirID]string) error {
	if svc.Name == "" {
		return wrapErr("install registry", errors.New("service has no name"))
	}

	base, ok := destDirs[svc.BinaryDirID]
	if !ok {
		return wrapErr("install registry", fmt.Errorf(
			"no destination directory supplied for DIRID %d (service %s)", svc.BinaryDirID, svc.Name))
	}

	components := pathComponents(base, svc.BinaryPath)
	if len(components) == 0 {
		return wrapErr("install registry", fmt.Errorf("empty ImagePath for service %s", svc.Name))
	}
	imagePath := `\` + strings.Join(components, `\`)

	return service.Install(servicesKey, service.Service{
		Name:            svc.Name,
		Type:            svc.ServiceType,
		Start:           svc.StartType,
		ErrorControl:    svc.ErrorControl,
		ImagePath:       imagePath,
		Group:           svc.LoadOrderGroup,
		DependOnGroup:   svc.DependOnGroup,
		DependOnService: svc.DependOnService,
	})
}

// mergeCriticalDeviceDatabase produces/merges one subkey per entry under
// cddbKey (the caller-supplied CriticalDeviceDatabase key itself), named per
// CriticalDeviceDatabaseSubkeyName, with the ClassGUID/Service values - see
// the CriticalDeviceDatabaseEntry doc comment in criticaldevicedatabase.go
// for citations. It reuses the sibling service package's generic
// find-or-create/set-value navigation helpers rather than keeping a second,
// private copy of that logic.
func mergeCriticalDeviceDatabase(cddbKey *regf.Key, entries []CriticalDeviceDatabaseEntry) {
	for _, e := range entries {
		key := service.FindOrCreateSubkey(cddbKey, CriticalDeviceDatabaseSubkeyName(e.HardwareID))
		if e.ClassGuid != "" {
			service.SetValue(key, "ClassGUID", regf.RegSZ, stringToUTF16LE(e.ClassGuid))
		}
		if e.Service != "" {
			service.SetValue(key, "Service", regf.RegSZ, stringToUTF16LE(e.Service))
		}
	}
}

// InstallRegistry merges pkg's Services and CriticalDeviceDatabase registry
// registration (see (*Package).Services and criticaldevicedatabase.go's
// CriticalDeviceDatabaseEntries) into currentControlSet, the root of the
// SYSTEM hive's current control set (e.g. as resolved by
// service.CurrentControlSet). It creates/reuses a "Services" subkey directly
// under currentControlSet, and a "CriticalDeviceDatabase" subkey under
// currentControlSet's "Control" subkey, matching the documented
// HKLM\SYSTEM\CurrentControlSet\Services / Control\CriticalDeviceDatabase
// layout.
//
// destDirs has the same shape and meaning as Install's destDirs parameter
// (install.go): it maps each DIRID used by any service's ServiceBinary entry
// to the image-relative directory path that DIRID resolves to. It is an
// error for a service's ServiceBinary DIRID to be absent from destDirs.
//
// Like Install, InstallRegistry is safe to call more than once with the
// same package against the same tree: the underlying find-or-create/
// set-value helpers (now in the sibling service package) find-or-create
// rather than blindly append, so a second call updates (rather than
// duplicates) the same Services\<name> and CriticalDeviceDatabase\<hwid>
// subkeys/values.
func InstallRegistry(currentControlSet *regf.Key, pkg *Package, destDirs map[DirID]string, platform string) error {
	if currentControlSet == nil {
		return wrapErr("install registry", errors.New("nil current control set key"))
	}
	if pkg == nil {
		return wrapErr("install registry", errors.New("nil package"))
	}

	services, err := pkg.Services(platform)
	if err != nil {
		return err
	}
	cdEntries, err := pkg.CriticalDeviceDatabaseEntries(platform)
	if err != nil {
		return err
	}

	servicesKey := service.FindOrCreateSubkey(currentControlSet, "Services")
	for _, svc := range services {
		if err := mergeServiceInstall(servicesKey, svc, destDirs); err != nil {
			return err
		}
	}

	controlKey := service.FindOrCreateSubkey(currentControlSet, "Control")
	cddbKey := service.FindOrCreateSubkey(controlKey, "CriticalDeviceDatabase")
	mergeCriticalDeviceDatabase(cddbKey, cdEntries)

	return nil
}
