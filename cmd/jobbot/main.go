// Command jobbot sends filtered LinkedIn vacancies to Discord.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/akmalfairuz/job-hunter/internal/jobbot"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config, err := jobbot.LoadConfig()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := jobbot.Run(ctx, config, logger); err != nil {
		logger.Error("jobbot stopped", "error", err)
		os.Exit(1)
	}
}
