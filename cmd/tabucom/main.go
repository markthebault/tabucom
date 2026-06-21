/*
This file is the executable entry point for the Tabucom service.
It loads environment configuration, constructs the internal HTTP server,
applies production timeouts, and coordinates graceful process shutdown.
It depends on the standard HTTP, signal, logging, and context packages,
plus internal/server for all hosting and persistence behavior.
*/
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/markthebault/tabucom/internal/server"
)

func main() {
	cfg, err := server.ConfigFromEnv()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	s, err := server.New(cfg)
	if err != nil {
		slog.Error("initialize server", "error", err)
		os.Exit(1)
	}
	defer s.Close()

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		slog.Info("listening", "address", cfg.ListenAddr, "data_dir", cfg.DataDir)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("serve", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("graceful shutdown failed", "error", err)
		_ = httpServer.Close()
	}
}
