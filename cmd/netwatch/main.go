// Command netwatch runs the Netwatch REST API + web GUI: a concurrent
// network scanner with optional Postgres-backed history.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lavicitor/netwatch/internal/api"
	"github.com/lavicitor/netwatch/internal/config"
	"github.com/lavicitor/netwatch/internal/scanner"
	"github.com/lavicitor/netwatch/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st *store.Store
	if !cfg.DB.Configured() {
		logger.Info("no database configured, running with in-memory results only")
	} else if s, err := store.Open(ctx, cfg.DB); err != nil {
		logger.Warn("database configured but connection failed, running with in-memory results only", "reason", err)
	} else {
		st = s
		defer st.Close()
		logger.Info("connected to database")
	}

	sc := scanner.New()
	sc.Ports = cfg.Ports
	srv := api.NewServer(cfg, sc, st, logger)

	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: srv.Routes(),
	}

	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
