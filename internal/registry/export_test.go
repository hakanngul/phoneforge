package registry

import "time"

// DiscoverTimeoutForTest swaps DiscoverTimeout and returns the old value.
func DiscoverTimeoutForTest(d time.Duration) time.Duration {
	old := DiscoverTimeout
	DiscoverTimeout = d
	return old
}
