package avcapture

import "os"

// envDisable returns PF_AVCAPTURE ("0"/"off" disables the native capture path).
func envDisable() string { return os.Getenv("PF_AVCAPTURE") }
