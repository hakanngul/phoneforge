// Command phoneforge is the command-line client for a running PhoneForge server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"phoneforge/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := cli.Main(ctx, os.Args[1:], cli.Env{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
	})
	stop()
	os.Exit(code)
}
