// Command phoneforged runs the PhoneForge headless server: the local REST API
// (/api/v1) and the embedded MCP server (/mcp) that an AI agent connects to.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"phoneforge/frontend"
	"phoneforge/internal/config"
	"phoneforge/internal/server"
)

func main() {
	cfg := config.Default()
	opts := server.Options{WebFS: frontend.FS()}
	flag.StringVar(&opts.Addr, "addr", cfg.Addr, "listen address")
	flag.StringVar(&opts.APIToken, "token", "", "optional API token required for non-loopback requests")
	flag.StringVar(&opts.ADBPath, "adb", "", "path to adb (default: adb in PATH)")
	flag.StringVar(&opts.WebDir, "webdir", "frontend", "directory of the web UI to serve at / (falls back to the embedded UI if missing)")
	flag.Parse()

	srv, err := server.Start(context.Background(), opts)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case <-srv.Done():
	}
	log.Println("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}
