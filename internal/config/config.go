// Package config holds Pocketlab runtime configuration.
package config

import "pocketlab/internal/version"

// Config is the server configuration.
type Config struct {
	Addr     string // listen address, default 127.0.0.1:8700
	APIToken string // optional; required for non-loopback requests
	Version  string
}

// Default returns the default configuration.
func Default() Config {
	return Config{Addr: "127.0.0.1:8700", Version: version.Version}
}
