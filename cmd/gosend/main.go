package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gosend/internal/app"
	"gosend/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:], os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(cfg, logger)
	if err != nil {
		logger.Error("initialize application", "error", err)
		os.Exit(1)
	}

	logger.Info(
		"starting GoSend",
		"alias", cfg.Alias,
		"web_address", cfg.WebAddress,
		"localsend_port", cfg.LocalSendPort,
		"send_directory", cfg.SendDirectory,
		"receive_directory", cfg.ReceiveDirectory,
	)

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}
