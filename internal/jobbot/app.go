package jobbot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/akmalfairuz/job-hunter/finder"
)

// Run starts the bot and blocks until the context is cancelled.
func Run(ctx context.Context, config Config, logger *slog.Logger) error {
	store, err := OpenStore(ctx, config.Database)
	if err != nil {
		return err
	}
	defer store.Close()

	evaluator, err := NewEvaluator(config.LLM)
	if err != nil {
		return fmt.Errorf("create LLM evaluator: %w", err)
	}
	discord, err := NewDiscordService(config.Discord, store, logger)
	if err != nil {
		return err
	}
	searcher := finder.NewClient(finder.ClientConfig{HTTPClient: &http.Client{}, UserAgent: config.LinkedIn.UserAgent})
	runner := NewRunner(store, searcher, evaluator, discord, config.Scheduler, config.Discord.AllowedGuildIDs, logger)
	discord.SetRunner(runner)
	if err := discord.Start(); err != nil {
		return err
	}
	defer discord.Close()

	logger.Info("jobbot started", "scheduler_interval", config.Scheduler.Interval)
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		RunScheduler(ctx, runner, config.Scheduler.Interval, config.Scheduler.RunTimeout, logger)
	}()
	<-ctx.Done()
	logger.Info("jobbot stopping")
	<-schedulerDone
	return nil
}
