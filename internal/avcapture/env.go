package avcapture

import "pocketlab/internal/brand"

// envDisable returns PL_AVCAPTURE ("0"/"off" disables the native capture path).
func envDisable() string { return brand.Getenv("PL_AVCAPTURE") }
