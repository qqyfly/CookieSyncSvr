package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/qqyfly/CookieSyncSvr/internal/config"
	"github.com/qqyfly/CookieSyncSvr/internal/crypto"
	"github.com/qqyfly/CookieSyncSvr/internal/mqtt"
	"github.com/qqyfly/CookieSyncSvr/internal/repository"
	"github.com/qqyfly/CookieSyncSvr/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := config.Load()

	cipher, err := crypto.NewCipher(cfg.EncryptionKey)
	if err != nil {
		logger.Error("failed to initialize encryption cipher", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var repo repository.SiteCookieRepository
	switch cfg.DBDriver {
	case "sqlite", "sqlite3":
		repo, err = repository.NewSQLiteRepository(cfg.DatabaseURL, cipher)
	case "postgres", "postgresql", "pgx":
		repo, err = repository.NewPostgresRepository(ctx, cfg.DatabaseURL, cipher)
	default:
		err = fmt.Errorf("unsupported DB_DRIVER %q (use 'sqlite' or 'postgres')", cfg.DBDriver)
	}
	if err != nil {
		logger.Error("failed to initialize storage repository", "driver", cfg.DBDriver, "err", err)
		os.Exit(1)
	}
	defer func() {
		_ = repo.Close()
	}()

	publisher, err := mqtt.NewPublisher(cfg.MQTTBrokerURL, cfg.MQTTTopic, cfg.MQTTUsername, cfg.MQTTPassword, logger)
	if err != nil {
		logger.Error("failed to initialize MQTT publisher", "err", err)
		os.Exit(1)
	}
	defer publisher.Close()

	srvHandler := server.New(cfg, repo, publisher, logger)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srvHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting CookieSyncSvr",
			"listen", cfg.ListenAddr,
			"db_driver", cfg.DBDriver,
			"auth_enabled", cfg.APIKey != "",
			"encryption_enabled", cfg.EncryptionKey != "",
			"mqtt_enabled", cfg.MQTTBrokerURL != "",
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received shutdown signal", "signal", sig.String())
	case err := <-errCh:
		logger.Error("HTTP server error", "err", err)
		os.Exit(1)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	logger.Info("CookieSyncSvr stopped")
}
