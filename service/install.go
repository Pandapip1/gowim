package service

import (
	"errors"

	"github.com/gavin-john/gowim/regf"
)

// Install produces/merges a Services\<svc.Name> subkey under servicesKey
// (which the caller has already navigated/created, e.g. via
// CurrentControlSet plus FindOrCreateSubkey(currentControlSet, "Services"))
// with the well-known service registration values - see "HKLM\SYSTEM\
// CurrentControlSet\Services Registry Tree",
// https://learn.microsoft.com/windows-hardware/drivers/install/hklm-system-currentcontrolset-services-registry-tree
// (Start/Type/ErrorControl/ImagePath), corroborated for ImagePath's
// REG_EXPAND_SZ type by that page's own text ("Windows creates this value by
// using the required ServiceBinary entry") together with real-world driver
// service registrations, which consistently record ImagePath as
// REG_EXPAND_SZ (e.g. "\SystemRoot\System32\drivers\<name>.sys", which must
// expand %SystemRoot%) rather than REG_SZ. Group/DependOnGroup/
// DependOnService are not on that page but are the well-known SCM service
// key value names for CreateService's lpLoadOrderGroup/lpDependencies
// parameters - see "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// whose Remarks section's value table lists Group, DependOnGroup, and
// DependOnService alongside Type/Start/ErrorControl/ImagePath as the values
// CreateService is documented to persist under this same Services\<name>
// key.
//
// Install is safe to call more than once with the same svc.Name against the
// same tree: it finds-or-creates rather than blindly appending, so a second
// call updates (rather than duplicates) the same Services\<name> subkey and
// its values, and clears (rather than leaves stale) any of Group/
// DependOnGroup/DependOnService that svc no longer sets.
func Install(servicesKey *regf.Key, svc Service) error {
	if servicesKey == nil {
		return wrapErr("install", errors.New("nil services key"))
	}
	if svc.Name == "" {
		return wrapErr("install", errors.New("service has no name"))
	}
	if svc.ImagePath == "" {
		return wrapErr("install", errors.New("service "+svc.Name+" has no ImagePath"))
	}

	key := FindOrCreateSubkey(servicesKey, svc.Name)

	SetValue(key, "Type", regf.RegDWORD, uint32LEBytes(svc.Type))
	SetValue(key, "Start", regf.RegDWORD, uint32LEBytes(svc.Start))
	SetValue(key, "ErrorControl", regf.RegDWORD, uint32LEBytes(svc.ErrorControl))
	SetValue(key, "ImagePath", regf.RegExpandSZ, stringToUTF16LE(svc.ImagePath))

	if svc.Group != "" {
		SetValue(key, "Group", regf.RegSZ, stringToUTF16LE(svc.Group))
	} else {
		RemoveValue(key, "Group")
	}
	if len(svc.DependOnGroup) > 0 {
		SetValue(key, "DependOnGroup", regf.RegMultiSZ, multiSZBytes(svc.DependOnGroup))
	} else {
		RemoveValue(key, "DependOnGroup")
	}
	if len(svc.DependOnService) > 0 {
		SetValue(key, "DependOnService", regf.RegMultiSZ, multiSZBytes(svc.DependOnService))
	} else {
		RemoveValue(key, "DependOnService")
	}

	return nil
}
