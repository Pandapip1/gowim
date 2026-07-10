// Package service models the Windows Service Control Manager's (SCM)
// registry schema - the HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services
// key tree that CreateService documents itself as writing to - and merges an
// already-resolved Service registration into a caller-supplied *regf.Key
// tree, using the sibling regf package
// (github.com/Pandapip1/gowim/regf) purely as a plain Key/Value struct tree,
// exactly as the sibling driver package's install.go builds *wim.DirEntry
// trees by hand with no special "tree API" from the wim package.
//
// This package depends on nothing but the Go standard library and regf, so
// that "add a service registration to a registry hive" is usable entirely on
// its own - without pulling in the much heavier dependency set (inf, cat, pe,
// wim) the sibling driver package needs purely to parse driver packages, not
// to define what a Windows service registration looks like in the registry.
//
// The values Install/Modify write (Type/Start/ErrorControl/ImagePath/Group/
// DisplayName/Description/ObjectName/DependOnGroup/DependOnService) and the
// Type/Start/ErrorControl constants below are cross-checked against the
// official Win32 CreateServiceW/ChangeServiceConfigW/ChangeServiceConfig2W
// function documentation on Microsoft Learn - see Install's, Service's, and
// the constants' doc comments for the precise citations - as of 2026-07-10.
//
// Beyond Install, this package can also read an existing Services\<name>
// registration back into a Service (Read), update one that must already
// exist (Modify), remove one entirely (Delete), and change just its start
// type (SetStartType/Enable/Disable) - all of these, like Install, only
// read/write a caller-supplied *regf.Key tree; none of them talk to a live
// service control manager (see the non-goals below).
//
// It deliberately does NOT implement:
//
//   - Any live SCM API semantics: starting, stopping, querying, or otherwise
//     controlling a running service (what CreateService/OpenService/
//     StartService/ControlService/QueryServiceStatus etc. do against a live
//     service control manager). This package - including Read/Modify/
//     Delete/SetStartType/Enable/Disable, not just Install - only reads/
//     writes the on-disk registry shape those APIs are documented to
//     persist; it never talks to a running SCM.
//   - The SYSTEM hive's DriverDatabase key tree, the Enum device-instance
//     tree, or INFCACHE.1 - none of these are a "service" concept at all;
//     see the sibling driver package's README
//     (https://github.com/Pandapip1/gowim/tree/main/driver#readme) for why
//     driver does not implement them either.
//   - CriticalDeviceDatabase: registering a hardware ID against a service
//     before PnP ever sees the device is a PnP/driver-install concept, not a
//     generic "service" concept, and stays entirely in the sibling driver
//     package (see driver/criticaldevicedatabase.go).
//   - Registry-hive file I/O (deciding which hive file to open, backing it
//     up, replacing it): this package only produces/merges/reads
//     regf.Key/regf.Value structures given an already-loaded (or
//     freshly-built) *regf.Key tree; the caller handles file I/O, exactly as
//     regf itself does not read/write files directly.
//   - Resolving *where a service's binary comes from*: an INF's
//     "%dirid%\path" token (see the driver package's DirID/ServiceInstall),
//     a plain absolute path, or anything else. That resolution is entirely
//     the caller's job - this package only writes an already-resolved
//     Service.ImagePath string into the registry.
//   - Recovery/failure-action configuration: ChangeServiceConfig2's
//     SERVICE_CONFIG_FAILURE_ACTIONS info level and the registry's
//     FailureActions (REG_BINARY) value, plus the related
//     FailureActionsOnNonCrashFailures/RebootMessage values. This was
//     actually checked, not assumed: the SERVICE_FAILURE_ACTIONSW structure
//     docs
//     (https://learn.microsoft.com/windows/win32/api/winsvc/ns-winsvc-service_failure_actionsw)
//     document the in-memory C struct that ChangeServiceConfig2/
//     QueryServiceConfig2 marshal to/from, but no authoritative Microsoft
//     source documents the actual on-disk byte layout of the FailureActions
//     REG_BINARY registry value itself (as distinct from that API-facing
//     struct shape); third-party notes such as the winreg-kb project's
//     "Services and drivers" page
//     (https://winreg-kb.readthedocs.io/en/latest/sources/system-keys/Services-and-drivers.html)
//     list FailureActions among the value names present under a service key
//     but document no format for it, confirming the gap rather than filling
//     it. This package therefore does not implement encoding/decoding
//     FailureActions, mirroring the sibling driver package's
//     DriverStore-hash and DriverDatabase non-goals: an undocumented
//     on-disk format is not something this package will guess at.
//   - The service account's *password*: LSA secrets in the SECURITY hive -
//     a separate, far more sensitive encrypted-blob mechanism, not a plain
//     Services\<name> registry value - are entirely out of scope; see
//     Service.ObjectName's doc comment.
package service

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned (wrapped, so callers should use errors.Is) by
// Read, Modify, Delete, SetStartType, Enable, and Disable when the named
// service does not have an existing Services\<name> subkey to operate on -
// deliberately a hard failure rather than a silent no-op, since a
// typo'd/missing service name should surface immediately.
var ErrNotFound = errors.New("service not found")

// Service is a fully-resolved Windows service registration: every field a
// caller would pass to CreateService's dwServiceType/dwStartType/
// dwErrorControl/lpBinaryPathName/lpLoadOrderGroup/lpDependencies
// parameters, and hence every value Install writes under a
// Services\<Name> registry key (see Install's doc comment). Unlike an INF's
// AddService-derived service-install-section (the sibling driver package's
// ServiceInstall type), Service.ImagePath is already a plain, fully-resolved
// path string - this package has no "dirid" concept; that INF-specific
// resolution step is entirely the caller's responsibility (see the package
// doc's non-goals).
type Service struct {
	// Name is the service's name - the Services subkey name Install
	// creates/updates.
	Name string
	// Type is the service type, one of the Type* constants (or another
	// SERVICE_* value CreateService documents that this package has no named
	// constant for).
	Type uint32
	// Start is the service start type, one of the Start* constants.
	Start uint32
	// ErrorControl is the severity of/action taken on a startup failure, one
	// of the Error* constants.
	ErrorControl uint32
	// ImagePath is the fully-resolved path to the service's binary (what
	// CreateService's lpBinaryPathName parameter, and hence the ImagePath
	// registry value, hold) - e.g.
	// `\Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_xxxxxxxx\driver.sys`
	// for a driver service, or a plain absolute path plus arguments for a
	// Win32 service. Must be non-empty.
	ImagePath string
	// Group is the load ordering group this service belongs to (
	// CreateService's lpLoadOrderGroup parameter), or "" for none.
	Group string
	// DisplayName is the friendly name applications and the Services
	// snap-in show for this service (CreateService's/ChangeServiceConfig's
	// lpDisplayName parameter, and hence the DisplayName registry value) -
	// see "ChangeServiceConfigW function (winsvc.h)",
	// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-changeserviceconfigw,
	// the "[in, optional] lpDisplayName" section: "The display name to be
	// used by applications to identify the service for its users." "" means
	// not written / cleared on update, exactly like Group.
	DisplayName string
	// Description is the service's descriptive comment shown by the
	// Services snap-in (ChangeServiceConfig2's SERVICE_CONFIG_DESCRIPTION
	// info level / the SERVICE_DESCRIPTIONW structure's lpDescription
	// member, and hence the Description registry value) - written as
	// REG_SZ, per that structure's own documented size limit: "The service
	// description must not exceed the size of a registry value of type
	// REG_SZ." See "SERVICE_DESCRIPTIONW (winsvc.h)",
	// https://learn.microsoft.com/windows/win32/api/winsvc/ns-winsvc-service_descriptionw.
	// "" means not written / cleared on update.
	Description string
	// ObjectName is the "Log On As" account name under which the service
	// runs (CreateService's/ChangeServiceConfig's lpServiceStartName
	// parameter, and hence the ObjectName registry value) - e.g.
	// "LocalSystem", `NT AUTHORITY\LocalService`, or `.\someuser`. See
	// "ChangeServiceConfigW function (winsvc.h)",
	// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-changeserviceconfigw,
	// the "[in, optional] lpServiceStartName" section. "" means not written
	// / cleared on update, exactly like Group/DisplayName/Description.
	//
	// ObjectName is JUST the account name string. The account's *password*
	// (what ChangeServiceConfig's lpPassword parameter sets) is stored via
	// LSA secrets in the SECURITY hive - a completely separate, far more
	// sensitive on-disk mechanism (per-secret encrypted blobs, not a plain
	// registry value under Services\<name>) that this package does NOT
	// implement; see the package doc's non-goals.
	ObjectName string
	// DependOnGroup lists the load-order-group names this service depends on
	// (the group-name entries of CreateService's lpDependencies parameter,
	// conventionally prefixed with SC_GROUP_IDENTIFIER on the wire - see
	// Install's doc comment), or nil for none.
	DependOnGroup []string
	// DependOnService lists the plain service names this service depends on
	// (the service-name entries of CreateService's lpDependencies parameter),
	// or nil for none.
	DependOnService []string
}

// Service type constants (the dwServiceType parameter/Type registry value),
// from "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// the "[in] dwServiceType" section's table (verified 2026-07-10).
const (
	// TypeKernelDriver (0x00000001) is SERVICE_KERNEL_DRIVER: "Driver
	// service."
	TypeKernelDriver uint32 = 0x00000001
	// TypeFileSystemDriver (0x00000002) is SERVICE_FILE_SYSTEM_DRIVER: "File
	// system driver service."
	TypeFileSystemDriver uint32 = 0x00000002
	// TypeWin32OwnProcess (0x00000010) is SERVICE_WIN32_OWN_PROCESS:
	// "Service that runs in its own process."
	TypeWin32OwnProcess uint32 = 0x00000010
	// TypeWin32ShareProcess (0x00000020) is SERVICE_WIN32_SHARE_PROCESS:
	// "Service that shares a process with one or more other services."
	TypeWin32ShareProcess uint32 = 0x00000020
)

// Service start-type constants (the dwStartType parameter/Start registry
// value), from "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// the "[in] dwStartType" section's table (verified 2026-07-10).
const (
	// StartBoot (0x00000000) is SERVICE_BOOT_START: "A device driver started
	// by the system loader. This value is valid only for driver services."
	StartBoot uint32 = 0x00000000
	// StartSystem (0x00000001) is SERVICE_SYSTEM_START: "A device driver
	// started by the IoInitSystem function. This value is valid only for
	// driver services."
	StartSystem uint32 = 0x00000001
	// StartAuto (0x00000002) is SERVICE_AUTO_START: "A service started
	// automatically by the service control manager during system startup."
	StartAuto uint32 = 0x00000002
	// StartDemand (0x00000003) is SERVICE_DEMAND_START: "A service started
	// by the service control manager when a process calls the StartService
	// function."
	StartDemand uint32 = 0x00000003
	// StartDisabled (0x00000004) is SERVICE_DISABLED: "A service that cannot
	// be started."
	StartDisabled uint32 = 0x00000004
)

// Service error-control constants (the dwErrorControl parameter/
// ErrorControl registry value), from "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// the "[in] dwErrorControl" section's table (verified 2026-07-10).
const (
	// ErrorIgnore (0x00000000) is SERVICE_ERROR_IGNORE: "The startup program
	// ignores the error and continues the startup operation."
	ErrorIgnore uint32 = 0x00000000
	// ErrorNormal (0x00000001) is SERVICE_ERROR_NORMAL: "The startup program
	// logs the error in the event log but continues the startup operation."
	ErrorNormal uint32 = 0x00000001
	// ErrorSevere (0x00000002) is SERVICE_ERROR_SEVERE: "The startup program
	// logs the error in the event log. If the last-known-good configuration
	// is being started, the startup operation continues. Otherwise, the
	// system is restarted with the last-known-good configuration."
	ErrorSevere uint32 = 0x00000002
	// ErrorCritical (0x00000003) is SERVICE_ERROR_CRITICAL: "...If the
	// last-known-good configuration is being started, the startup operation
	// fails. Otherwise, the system is restarted with the last-known good
	// configuration."
	ErrorCritical uint32 = 0x00000003
)

// wrapErr is a small helper for adding context to errors without pulling in
// a dependency, mirroring the sibling regf/driver packages' own wrapErr.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("service: %s: %w", what, err)
}
