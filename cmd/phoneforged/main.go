// Command phoneforged runs the PhoneForge headless server: the local REST API
// (/api/v1) and the embedded MCP server (/mcp) that an AI agent connects to.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"phoneforge/frontend"
	"phoneforge/internal/config"
	"phoneforge/internal/server"
)

func main() {
	cfg := config.Default()
	opts := server.Options{WebFS: frontend.FS(), Owner: "cli", StartedVia: "phoneforged"}
	if v := os.Getenv("PHONEFORGE_STARTED_VIA"); v != "" {
		opts.StartedVia = v // set by `phoneforge server start`
	}
	flag.StringVar(&opts.Addr, "addr", cfg.Addr, "listen address")
	flag.StringVar(&opts.APIToken, "token", "", "optional API token required for non-loopback requests")
	flag.StringVar(&opts.ADBPath, "adb", "", "path to adb (default: adb in PATH)")
	flag.StringVar(&opts.WebDir, "webdir", "frontend", "directory of the web UI to serve at / (falls back to the embedded UI if missing)")
	flag.Parse()

	srv, err := server.Start(context.Background(), opts)
	if err != nil {
		log.Fatalf("server: %v (the failure is recorded in %s)", err, server.PhoneforgeHome())
	}
	// A panic on the main goroutine is recorded (with the log tail and stack) before exiting.
	defer func() {
		if r := recover(); r != nil {
			srv.RecordCrash(fmt.Sprint(r))
			fmt.Fprintf(os.Stderr, "panic: %v\n%s", r, debug.Stack())
			os.Exit(2)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	select {
	case sig := <-stop:
		name := sigName(sig)
		log.Printf("received signal %s", name)
		srv.Lifecycle().SetStopReason("received signal "+name, "signal:"+name, name)
	case <-srv.ShutdownRequested():
		log.Println("shutdown requested via API")
	case <-srv.Done():
		srv.SetStopReason("HTTP server stopped serving", "internal")
	}
	log.Println("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func sigName(s os.Signal) string {
	switch s {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	case os.Interrupt:
		return "SIGINT"
	}
	return s.String()
}
