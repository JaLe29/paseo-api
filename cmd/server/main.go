// Command paseo-api is an HTTP service that exposes a paseo daemon over REST.
// It replaces invoking the paseo CLI and spawning subprocesses (e.g. in ChemCheck):
// a client just POSTs a prompt (and images) to this API and gets the agent's
// transcript back.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"paseo-api/internal/config"
	"paseo-api/internal/httpapi"
	"paseo-api/internal/paseo"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}

	client := paseo.New(paseo.Options{
		Host:            cfg.PaseoHost,
		Password:        cfg.PaseoPassword,
		DefaultProvider: cfg.DefaultProvider,
		DefaultModel:    cfg.DefaultModel,
		DefaultCwd:      cfg.DefaultCwd,
		DefaultMode:     cfg.DefaultMode,
		DefaultThinking: cfg.DefaultThinking,
		WaitTimeout:     cfg.WaitTimeout,
		ConnectTimeout:  cfg.ConnectTimeout,
		Log:             log,
	})

	srv := httpapi.New(cfg, client, log)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		log.Info("paseo-api starting", "addr", cfg.ListenAddr, "paseoHost", cfg.PaseoHost)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server crashed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Info("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Error("shutdown error", "err", err)
	}
}
