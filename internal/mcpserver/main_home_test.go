package mcpserver

import (
	"os"
	"testing"

	"pocketlab/internal/testhome"
)

func TestMain(m *testing.M) { os.Exit(testhome.Run(m.Run)) }
