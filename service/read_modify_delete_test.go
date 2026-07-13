package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Pandapip1/gowim/regf"
)

// fullTestService returns a Service exercising every field Install/Modify
// know how to write, including the new DisplayName/Description/ObjectName
// fields and both dependency lists, for round-trip testing.
func fullTestService() Service {
	return Service{
		Name:            "ContosoSvc",
		Type:            TypeWin32OwnProcess,
		Start:           StartAuto,
		ErrorControl:    ErrorNormal,
		ImagePath:       `C:\Program Files\Contoso\contosvc.exe`,
		Group:           "Extended Base",
		DisplayName:     "Contoso Service",
		Description:     "Does contoso things.",
		ObjectName:      `NT AUTHORITY\LocalService`,
		DependOnGroup:   []string{"NetBIOSGroup"},
		DependOnService: []string{"RpcSs"},
	}
}

func newTestServicesKey(t *testing.T) *regf.Key {
	t.Helper()
	root := buildSystemHiveRoot()
	cs, err := CurrentControlSet(root)
	if err != nil {
		t.Fatalf("CurrentControlSet: %v", err)
	}
	return cs.FindOrCreateSubkey("Services")
}

func TestReadRoundTrip(t *testing.T) {
	servicesKey := newTestServicesKey(t)
	svc := fullTestService()

	if err := Install(servicesKey, svc); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := Read(servicesKey, svc.Name)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, svc) {
		t.Errorf("Read round-trip = %+v, want %+v", got, svc)
	}
}

func TestReadNotFound(t *testing.T) {
	servicesKey := newTestServicesKey(t)

	_, err := Read(servicesKey, "NoSuchService")
	if err == nil {
		t.Fatal("expected an error reading a nonexistent service")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Read error = %v, want it to wrap ErrNotFound", err)
	}
}

func TestReadErrors(t *testing.T) {
	servicesKey := newTestServicesKey(t)

	if _, err := Read(nil, "X"); err == nil {
		t.Error("expected an error for a nil services key")
	}
	if _, err := Read(servicesKey, ""); err == nil {
		t.Error("expected an error for an empty service name")
	}
}

func TestModifyNotFoundDoesNotCreate(t *testing.T) {
	servicesKey := newTestServicesKey(t)
	svc := fullTestService()

	err := Modify(servicesKey, svc)
	if err == nil {
		t.Fatal("expected an error modifying a nonexistent service")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Modify error = %v, want it to wrap ErrNotFound", err)
	}
	if servicesKey.Subkey(svc.Name) != nil {
		t.Error("Modify on a nonexistent service must not create one")
	}
}

func TestModifyUpdatesExisting(t *testing.T) {
	servicesKey := newTestServicesKey(t)
	svc := fullTestService()

	if err := Install(servicesKey, svc); err != nil {
		t.Fatalf("Install: %v", err)
	}

	updated := svc
	updated.Start = StartDemand
	updated.DisplayName = "Contoso Service (renamed)"
	updated.Description = "" // clear a previously-set optional field
	updated.DependOnGroup = nil

	if err := Modify(servicesKey, updated); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	got, err := Read(servicesKey, svc.Name)
	if err != nil {
		t.Fatalf("Read after Modify: %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Errorf("Read after Modify = %+v, want %+v", got, updated)
	}

	svcKey := servicesKey.Subkey(svc.Name)
	if svcKey.Value("Description") != nil {
		t.Error("Description value still present after Modify cleared it")
	}
	if svcKey.Value("DependOnGroup") != nil {
		t.Error("DependOnGroup value still present after Modify cleared it")
	}
}

func TestDelete(t *testing.T) {
	servicesKey := newTestServicesKey(t)
	svc := fullTestService()

	if err := Install(servicesKey, svc); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := Delete(servicesKey, svc.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if servicesKey.Subkey(svc.Name) != nil {
		t.Error("Services subkey still present after Delete")
	}

	// A second Delete of the same (now-gone) name must fail with
	// ErrNotFound rather than silently succeeding.
	err := Delete(servicesKey, svc.Name)
	if err == nil {
		t.Fatal("expected an error deleting an already-deleted service")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete error = %v, want it to wrap ErrNotFound", err)
	}

	// Deleting a name that never existed must also fail with ErrNotFound.
	err = Delete(servicesKey, "NeverExisted")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete of a never-existing name error = %v, want it to wrap ErrNotFound", err)
	}
}

func TestDeleteErrors(t *testing.T) {
	if err := Delete(nil, "X"); err == nil {
		t.Error("expected an error for a nil services key")
	}
	servicesKey := newTestServicesKey(t)
	if err := Delete(servicesKey, ""); err == nil {
		t.Error("expected an error for an empty service name")
	}
}

func TestSetStartTypeEnableDisable(t *testing.T) {
	servicesKey := newTestServicesKey(t)
	svc := fullTestService()
	svc.Start = StartAuto

	if err := Install(servicesKey, svc); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := SetStartType(servicesKey, svc.Name, StartDemand); err != nil {
		t.Fatalf("SetStartType: %v", err)
	}
	got, err := Read(servicesKey, svc.Name)
	if err != nil {
		t.Fatalf("Read after SetStartType: %v", err)
	}
	want := svc
	want.Start = StartDemand
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after SetStartType(StartDemand) = %+v, want %+v (only Start should change)", got, want)
	}

	if err := Disable(servicesKey, svc.Name); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, err = Read(servicesKey, svc.Name)
	if err != nil {
		t.Fatalf("Read after Disable: %v", err)
	}
	want.Start = StartDisabled
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after Disable = %+v, want %+v (only Start should change)", got, want)
	}

	if err := Enable(servicesKey, svc.Name, StartAuto); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got, err = Read(servicesKey, svc.Name)
	if err != nil {
		t.Fatalf("Read after Enable: %v", err)
	}
	want.Start = StartAuto
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after Enable(StartAuto) = %+v, want %+v (only Start should change)", got, want)
	}

	if err := Enable(servicesKey, svc.Name, StartDisabled); err == nil {
		t.Error("expected Enable(..., StartDisabled) to return an error")
	}

	// SetStartType/Enable/Disable on a nonexistent service must all report
	// ErrNotFound.
	for _, call := range []func() error{
		func() error { return SetStartType(servicesKey, "NoSuchService", StartAuto) },
		func() error { return Enable(servicesKey, "NoSuchService", StartAuto) },
		func() error { return Disable(servicesKey, "NoSuchService") },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want it to wrap ErrNotFound", err)
		}
	}
}
