package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rvburov/private-gateway/internal/browser"
	"github.com/rvburov/private-gateway/internal/config"
	"github.com/rvburov/private-gateway/internal/httpapi"
	"github.com/rvburov/private-gateway/internal/storage"
	"github.com/rvburov/private-gateway/internal/xray"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: gateway-api serve|migrate|keygen")
	}
	switch os.Args[1] {
	case "keygen":
		return keygen()
	case "migrate":
		dsn := os.Getenv("GATEWAY_DATABASE_URL")
		if dsn == "" {
			return errors.New("GATEWAY_DATABASE_URL is required")
		}
		if err := storage.Migrate(dsn); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
		slog.Info("database migrations complete")
		return nil
	case "serve":
		return serve()
	default:
		return errors.New("usage: gateway-api serve|migrate|keygen")
	}
}
func keygen() error {
	var master, token [32]byte
	var short [8]byte
	for _, b := range [][]byte{master[:], token[:], short[:]} {
		if _, err := rand.Read(b); err != nil {
			return err
		}
	}
	reality, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"master_key": base64.StdEncoding.EncodeToString(master[:]), "api_token": hex.EncodeToString(token[:]), "reality_private_key": base64.RawURLEncoding.EncodeToString(reality.Bytes()), "short_id": hex.EncodeToString(short[:])})
}
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, cfg.DSN, cfg.Cipher)
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.EnsureNode(ctx, cfg.Node); err != nil {
		return errors.New("cannot initialize node; run migrations and verify master key")
	}
	var browserManager httpapi.Browser = browser.DisabledManager{}
	if cfg.BrowserWorkerURL != "" {
		browserManager, err = browser.NewRemoteManager(browser.RemoteOptions{
			URL: cfg.BrowserWorkerURL, Token: cfg.BrowserWorkerToken, PublicURL: cfg.BrowserPublicURL,
		})
		if err != nil {
			return err
		}
	}
	handler := httpapi.New(store, xray.NewManager(cfg.Xray), browserManager, httpapi.Options{Token: cfg.Token, WebDir: cfg.WebDir, XrayConfigured: len(cfg.Xray.RestartCommand) > 0})
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("cannot bind API loopback address; verify port availability")
	}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { slog.Info("gateway API listening", "address", cfg.Listen); done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stopCtx.Done():
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		return server.Shutdown(shutdownCtx)
	}
}
