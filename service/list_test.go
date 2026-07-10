package service

import (
	"reflect"
	"sort"
	"testing"
)

func TestList(t *testing.T) {
	servicesKey := newTestServicesKey(t)

	got, err := List(servicesKey)
	if err != nil {
		t.Fatalf("List (empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List (empty) = %v, want empty", got)
	}

	names := []string{"ContosoSvc", "AnotherSvc"}
	for _, name := range names {
		svc := fullTestService()
		svc.Name = name
		if err := Install(servicesKey, svc); err != nil {
			t.Fatalf("Install %q: %v", name, err)
		}
	}

	got, err = List(servicesKey)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(got)
	want := append([]string(nil), names...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}

	if err := Delete(servicesKey, "ContosoSvc"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = List(servicesKey)
	if err != nil {
		t.Fatalf("List after Delete: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"AnotherSvc"}) {
		t.Errorf("List after Delete = %v, want [AnotherSvc]", got)
	}
}

func TestListNilKey(t *testing.T) {
	if _, err := List(nil); err == nil {
		t.Error("List(nil) expected an error")
	}
}
