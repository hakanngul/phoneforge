package dsl

import "context"

// Optional device capabilities used by actions that the base
// device.Device contract cannot express. A driver opts in by implementing
// the interface; without it the step fails with device.ErrUnsupported.

// TwoFingerTapper performs a two-finger tap (two_finger_tap).
type TwoFingerTapper interface {
	TwoFingerTap(ctx context.Context, x, y int) error
}

// PathDragger performs a multi-point drag (drag_path).
type PathDragger interface {
	DragPath(ctx context.Context, points []PathPoint) error
}

// TextClearer clears the focused text field (clear, type clear_first).
type TextClearer interface {
	ClearText(ctx context.Context) error
}

// PickerSelector spins a picker wheel at (x, y) to value (select_picker).
type PickerSelector interface {
	SelectPickerValue(ctx context.Context, x, y int, value string) error
}

// LocationController sets and resets the simulated GPS location.
type LocationController interface {
	SetLocation(ctx context.Context, lat, lon float64) error
	ResetLocation(ctx context.Context) error
}

// BiometricsController simulates a biometric result (match, nomatch, enroll, unenroll).
type BiometricsController interface {
	Biometrics(ctx context.Context, state, sensor string) error
}

// FoldController changes the fold state of a foldable device.
type FoldController interface {
	Fold(ctx context.Context, state string, angle *float64) error
}

// SiriController sends a prompt to Siri and returns its reply.
type SiriController interface {
	Siri(ctx context.Context, prompt string) (string, error)
}
