package device

import "errors"

var (
	// ErrNotFound is returned when a device id is unknown.
	ErrNotFound = errors.New("device not found")
	// ErrUnsupported is returned by a driver that does not implement an action.
	ErrUnsupported = errors.New("operation not supported on this device")
	// ErrElementNotFound is returned by the DSL matcher when a predicate matches nothing.
	ErrElementNotFound = errors.New("element not found")
)
