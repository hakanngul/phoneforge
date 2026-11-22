// Command pocketlab is the command-line client for a running Pocketlab server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"pocketlab/internal/brand"
	"pocketlab/internal/cli"
)

func main() {
	brand.DataDir() // migrate ~/.phoneforge -> ~/.pocketlab (logged once) before anything reads the state dir
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
