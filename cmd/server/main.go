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

	"github.com/roymwxuk/mwx-go-url-shortener/internal/config"
	"github.com/roymwxuk/mwx-go-url-shortener/internal/httpapi"
	"github.com/roymwxuk/mwx-go-url-shortener/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st store.Store
	if cfg.DatabaseURL != "" {
		pg, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		st = pg
		log.Info("using postgres store")
	} else {
		st = store.NewMemoryStore()
		log.Warn("DATABASE_URL not set; using in-memory store (data is lost on restart)")
	}
	defer st.Close()

	api := httpapi.New(st, httpapi.Options{
		BaseURL:          cfg.BaseURL,
		TrustedProxyHops: cfg.TrustedProxyHops,
		Redirect:         httpapi.Limit{RPS: cfg.RedirectRPS, Burst: cfg.RedirectBurst},
		NotFound:         httpapi.Limit{RPS: cfg.NotFoundRPS, Burst: cfg.NotFoundBurst},
		API:              httpapi.Limit{RPS: cfg.APIRPS, Burst: cfg.APIBurst},
		BlockedHosts:     cfg.BlockedHosts,
	}, log)
	api.RunJanitor(ctx.Done())

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Routes(),
		MaxHeaderBytes:    16 << 10,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
