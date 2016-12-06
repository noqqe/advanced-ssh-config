package hooks

import (
	"fmt"
	"strings"

	composeyaml "github.com/docker/libcompose/yaml"
)

// Hooks represents a slice of Hook
type Hooks composeyaml.Stringorslice

// HookDriver represents a hook driver
type HookDriver interface {
	Run(RunArgs) error
	Close() error
}

// HookDrivers represents a slice of HookDriver
type HookDrivers []HookDriver

// RunArgs is a map of interface{}
type RunArgs interface{}

// InvokeAll calls all hooks
func (h *Hooks) InvokeAll(args RunArgs) (HookDrivers, error) {
	drivers := HookDrivers{}

	for _, expr := range *h {
		driver, err := New(expr)
		if err != nil {
			return nil, err
		}
		drivers = append(drivers, driver)
	}

	for _, driver := range drivers {
		if err := driver.Run(args); err != nil {
			return nil, err
		}
	}
	return drivers, nil
}

// Close closes all hook drivers and returns a slice of errs
func (hd *HookDrivers) Close() []error {
	var errs []error
	for _, driver := range *hd {
		if err := driver.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// New returns an HookDriver instance
func New(expr string) (HookDriver, error) {
	driverName := strings.Split(string(expr), " ")[0]
	param := strings.Join(strings.Split(string(expr), " ")[1:], " ")
	switch driverName {
	case "write":
		driver, err := NewWriteDriver(param)
		return driver, err
	case "exec":
		driver, err := NewExecDriver(param)
		return driver, err
	case "daemon":
		driver, err := NewDaemonDriver(param)
		return driver, err
	default:
		return nil, fmt.Errorf("No such driver %q", driverName)
	}
}
