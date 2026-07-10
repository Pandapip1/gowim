package service

import (
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// SetStartType changes an existing service's Start value (one of the
// Start* constants) without requiring the caller to reconstruct the whole
// Service: it reads the current registration (Read), changes just Start,
// and writes it back (Modify). Like Read/Modify, it returns an error
// wrapping ErrNotFound (checkable with errors.Is) if name has no existing
// Services subkey.
func SetStartType(servicesKey *regf.Key, name string, start uint32) error {
	svc, err := Read(servicesKey, name)
	if err != nil {
		return err
	}
	svc.Start = start
	return Modify(servicesKey, svc)
}

// Disable is sugar for SetStartType(servicesKey, name, StartDisabled) -
// SERVICE_DISABLED, "a service that cannot be started" (see StartDisabled's
// doc comment).
func Disable(servicesKey *regf.Key, name string) error {
	return SetStartType(servicesKey, name, StartDisabled)
}

// Enable is sugar for SetStartType, for re-enabling a disabled service to
// one of the non-disabled start types (StartBoot/StartSystem/StartAuto/
// StartDemand - "enable" is not single-valued, since a service could be
// re-enabled to any of these). It rejects start == StartDisabled with a
// clear error rather than silently disabling the service a function named
// Enable was called on.
func Enable(servicesKey *regf.Key, name string, start uint32) error {
	if start == StartDisabled {
		return wrapErr("enable", fmt.Errorf("service %q: StartDisabled passed to Enable; use Disable instead", name))
	}
	return SetStartType(servicesKey, name, start)
}
