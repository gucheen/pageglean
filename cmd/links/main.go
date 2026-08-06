package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"links/internal/app"
	"links/internal/backup"
	"links/internal/config"
	"links/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	data, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer data.Close()

	if len(args) == 0 || args[0] == "serve" {
		return serve(cfg, data)
	}
	if args[0] == "admin" {
		return admin(cfg, data, args[1:])
	}
	return fmt.Errorf("unknown command %q; use serve or admin", args[0])
}

func serve(cfg config.Config, data *store.Store) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	application, err := app.New(cfg, data, logger)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           application.Handler(),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	application.Start(workerCtx)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("Links started", "addr", cfg.Addr, "public_url", cfg.PublicURL, "rp_id", cfg.RPID)
		errCh <- server.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-signalCtx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func admin(cfg config.Config, data *store.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing admin command; use setup-link, recovery-link, backup, or verify-backup")
	}
	if args[0] == "backup" {
		return createBackup(cfg, data, args[1:])
	}
	if args[0] == "verify-backup" {
		return verifyBackup(args[1:])
	}
	kind := ""
	switch args[0] {
	case "setup-link":
		kind = "setup"
	case "recovery-link":
		kind = "recovery"
	default:
		return fmt.Errorf("unknown admin command %q", args[0])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	ttl := flags.Duration("ttl", 10*time.Minute, "one-time link lifetime")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *ttl < time.Minute || *ttl > time.Hour {
		return fmt.Errorf("ttl must be between 1m and 1h")
	}
	token, err := data.CreateAdminToken(context.Background(), kind, *ttl)
	if err != nil {
		return err
	}
	link, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return err
	}
	link.Path = "/setup"
	query := link.Query()
	query.Set("token", token)
	link.RawQuery = query.Encode()
	fmt.Println(link.String())
	return nil
}

func createBackup(cfg config.Config, data *store.Store, args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	defaultName := "links-backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	output := flags.String("output", defaultName, "backup archive path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if err := backup.Create(context.Background(), data, cfg.DataDir, absOutput); err != nil {
		return err
	}
	fmt.Println(absOutput)
	return nil
}

func verifyBackup(args []string) error {
	flags := flag.NewFlagSet("verify-backup", flag.ContinueOnError)
	input := flags.String("input", "", "backup archive path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("--input is required")
	}
	if err := backup.Verify(*input); err != nil {
		return err
	}
	fmt.Println("backup verified")
	return nil
}
