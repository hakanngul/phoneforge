// Package testhome isolates a test binary from the developer's real home:
// HOME and POCKETLAB_HOME point at a throwaway directory, so nothing under
// ~/.pocketlab can be read or written by `go test`.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"
)

// Run is for TestMain: `func TestMain(m *testing.M) { os.Exit(testhome.Run(m.Run)) }`.
func Run(run func() int) int {
	tmp, err := os.MkdirTemp("", "pocketlab-testhome-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	home := filepath.Join(tmp, "home")
	os.MkdirAll(home, 0o755)
	os.Setenv("HOME", home)
	os.Setenv("POCKETLAB_HOME", filepath.Join(home, ".pocketlab"))
	os.Unsetenv("PHONEFORGE_HOME")
	return run()
}
