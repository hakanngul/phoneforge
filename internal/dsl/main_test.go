package dsl

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	retryDelay = 2 * time.Millisecond
	alertPause = 0
	settleStable = 20 * time.Millisecond
	os.Exit(m.Run())
}
