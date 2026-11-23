package iosdrv

import "testing"

func TestIOSMajor(t *testing.T) {
	for in, want := range map[string]int{"26.5.2": 26, "17.0": 17, "16.7.1": 16, "": 0, "x": 0, " 18.1 ": 18} {
		if got := iosMajor(in); got != want {
			t.Errorf("%q: %d want %d", in, got, want)
		}
	}
}
