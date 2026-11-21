// Package webautomation owns the platform-neutral registry for web automation
// drivers. Computer Use is the default driver today; a CDP driver can be added
// later without changing callers or replacing the embedded browser preview.
package webautomation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const DefaultDriverID = "computer-use"

type DriverStatus struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Available    bool     `json:"available"`
	Platform     string   `json:"platform"`
	Capabilities []string `json:"capabilities"`
	Message      string   `json:"message,omitempty"`
}

type Driver interface {
	ID() string
	Status() DriverStatus
	Execute(toolName string, arguments map[string]any) (any, error)
}

var registry = struct {
	sync.RWMutex
	drivers map[string]Driver
}{drivers: make(map[string]Driver)}

func Register(driver Driver) error {
	if driver == nil {
		return errors.New("register web automation driver: driver is nil")
	}
	id := strings.TrimSpace(driver.ID())
	if id == "" {
		return errors.New("register web automation driver: id is required")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.drivers[id]; exists {
		return fmt.Errorf("register web automation driver %q: already registered", id)
	}
	registry.drivers[id] = driver
	return nil
}

func Get(id string) (Driver, bool) {
	registry.RLock()
	defer registry.RUnlock()
	driver, ok := registry.drivers[strings.TrimSpace(id)]
	return driver, ok
}

func Statuses() []DriverStatus {
	registry.RLock()
	drivers := make([]Driver, 0, len(registry.drivers))
	for _, driver := range registry.drivers {
		drivers = append(drivers, driver)
	}
	registry.RUnlock()

	statuses := make([]DriverStatus, 0, len(drivers))
	for _, driver := range drivers {
		status := driver.Status()
		status.ID = driver.ID()
		status.Capabilities = append([]string(nil), status.Capabilities...)
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}

func Execute(driverID, toolName string, arguments map[string]any) (any, error) {
	if strings.TrimSpace(driverID) == "" {
		driverID = DefaultDriverID
	}
	driver, ok := Get(driverID)
	if !ok {
		return nil, fmt.Errorf("web automation driver %q is not registered", driverID)
	}
	status := driver.Status()
	if !status.Available && !strings.HasSuffix(strings.TrimSpace(toolName), "get_status") {
		if strings.TrimSpace(status.Message) != "" {
			return nil, fmt.Errorf("web automation driver %q is unavailable: %s", driverID, status.Message)
		}
		return nil, fmt.Errorf("web automation driver %q is unavailable on %s", driverID, status.Platform)
	}
	return driver.Execute(toolName, arguments)
}
