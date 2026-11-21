package webautomation

import (
	"strings"
	"testing"
)

type testDriver struct {
	id        string
	available bool
}

func (d testDriver) ID() string { return d.id }
func (d testDriver) Status() DriverStatus {
	return DriverStatus{ID: d.id, Name: "Test", Available: d.available, Platform: "test", Capabilities: []string{"open-url"}}
}
func (d testDriver) Execute(toolName string, _ map[string]any) (any, error) {
	return toolName, nil
}

func TestRegistryExecutesNamedDriver(t *testing.T) {
	id := "test-registry-driver"
	if err := Register(testDriver{id: id, available: true}); err != nil {
		t.Fatalf("register driver: %v", err)
	}
	result, err := Execute(id, "computer.open_url", nil)
	if err != nil {
		t.Fatalf("execute driver: %v", err)
	}
	if result != "computer.open_url" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := Register(testDriver{id: id, available: true}); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestRegistryRejectsUnavailableDriver(t *testing.T) {
	id := "test-unavailable-driver"
	if err := Register(testDriver{id: id, available: false}); err != nil {
		t.Fatalf("register driver: %v", err)
	}
	_, err := Execute(id, "computer.open_url", nil)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}
