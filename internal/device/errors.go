package device

import "errors"

// WDARequiredMessage is the user-facing text of ErrWDARequired.
const WDARequiredMessage = "Start WDA to see the screen of iOS 17+ devices"

// ErrWDARequired means the action needs the WebDriverAgent bridge to be running
// (iOS 17+ screenshots without WDA).
var ErrWDARequired = errors.New(WDARequiredMessage)

// WDARequiredError is ErrWDARequired carrying the underlying technical cause.
// Its message stays friendly; the cause is available via Cause.
type WDARequiredError struct{ Cause error }

func (e *WDARequiredError) Error() string        { return WDARequiredMessage }
func (e *WDARequiredError) Is(target error) bool { return target == ErrWDARequired }
func (e *WDARequiredError) Unwrap() error        { return e.Cause }

var (
	// ErrNotFound is returned when a device id is unknown.
	ErrNotFound = errors.New("device not found")
	// ErrUnsupported is returned by a driver that does not implement an action.
	ErrUnsupported = errors.New("operation not supported on this device")
	// ErrElementNotFound is returned by the DSL matcher when a predicate matches nothing.
	ErrElementNotFound = errors.New("element not found")
)
