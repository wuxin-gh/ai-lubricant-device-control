// Command device-control-server is the reference self-hosted server for the
// device-control v0 protocol (see spec/protocol-v0.md).
//
// It is deliberately minimal: one binary, one JSON state file, no database. It
// ships no default upstream address of any kind — the operator runs it, and the
// device is pointed at it by the operator (PLAN.md §1).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"device-control/server/internal/httpapi"
	"device-control/server/internal/hub"
	"device-control/server/internal/store"
	"device-control/server/internal/wsdevice"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr      = flag.String("addr", "127.0.0.1:8787", "listen address (host:port)")
		statePath = flag.String("state", "device-control-state.json", "path to the JSON state file (0600)")
		verbose   = flag.Bool("v", false, "verbose (debug) logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// The admin API can drive a phone, so it is never allowed to be open. We
	// require the token via env rather than a flag to keep it out of the
	// process list.
	adminToken := os.Getenv("DEVICE_CONTROL_ADMIN_TOKEN")
	if adminToken == "" {
		return errors.New("DEVICE_CONTROL_ADMIN_TOKEN must be set (it guards the admin API, which can control a device)")
	}

	st, err := store.Open(*statePath)
	if err != nil {
		return err
	}

	h := hub.New()

	api := &httpapi.Server{Store: st, Hub: h, Log: log, AdminToken: adminToken}
	mux := http.NewServeMux()
	mux.Handle("/", api.Routes())
	mux.Handle("/ws/device", &wsdevice.Handler{Store: st, Hub: h, Log: log})

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
		// No WriteTimeout/ReadTimeout: they would kill long-lived WebSockets.
		// Liveness is handled by the protocol's own heartbeat (spec §7).
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "state", *statePath,
			"device_ws", "ws://"+*addr+"/ws/device")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
