// Command worker delivers durable events with bounded concurrency.
package main

import (
	"context"
	"fmt"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/observability"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/migrations"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		_, _ = fmt.Fprintf(os.Stdout, "worker %s\n", version)
		return
	}
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.LoadWorker(os.Getenv)
	if err != nil {
		return err
	}
	log := observability.NewLogger(cfg.LogLevel, cfg.LogFormat, os.Stdout)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	start, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	st, err := store.Open(start, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer st.Close()
	if cfg.MigrateOnStart {
		if err := st.Migrate(start, migrations.FS); err != nil {
			return err
		}
	}
	transport, err := stream.Open(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close() }()
	client := delivery.New(cfg.HTTPTimeout, cfg.AllowPrivateTargets)
	defer client.Close()
	w := worker.Worker{Store: st, Stream: transport, Client: client, Log: log, Concurrency: cfg.Workers, LeaseDuration: cfg.LeaseDuration, SweepInterval: cfg.SweepInterval}
	log.Info("worker started", "concurrency", cfg.Workers, "version", version)
	return w.Run(ctx)
}
