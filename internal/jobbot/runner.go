package jobbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/akmalfairuz/job-hunter/finder"
)

type Notifier interface {
	SendJob(context.Context, Notification) error
}

// DeliveryError distinguishes a Discord rejection that is known not to have
// created a message from an ambiguous transport failure. Ambiguous failures
// retain their database claim to favor no duplicates over possible retries.
type DeliveryError struct {
	Err         error
	SafeToRetry bool
}

func (failure *DeliveryError) Error() string { return failure.Err.Error() }
func (failure *DeliveryError) Unwrap() error { return failure.Err }

type Runner struct {
	repository    Repository
	searcher      finder.Searcher
	evaluator     Evaluator
	notifier      Notifier
	config        SchedulerConfig
	allowedGuilds map[string]struct{}
	logger        *slog.Logger
}

// ManualRunOptions contains overrides that apply only to one manual run.
type ManualRunOptions struct {
	HoursOld *int
}

func NewRunner(repository Repository, searcher finder.Searcher, evaluator Evaluator, notifier Notifier, config SchedulerConfig, allowedGuildIDs []string, logger *slog.Logger) *Runner {
	return &Runner{repository: repository, searcher: searcher, evaluator: evaluator, notifier: notifier, config: config, allowedGuilds: guildSet(allowedGuildIDs), logger: logger}
}

func (runner *Runner) RunScheduled(ctx context.Context) error {
	locked, err := runner.repository.WithSchedulerLock(ctx, func(lockContext context.Context) error {
		return runner.run(lockContext, nil, runner.config.HoursOld)
	})
	if err != nil {
		return err
	}
	if !locked {
		runner.logger.Info("scheduler run skipped because another instance owns the lock")
	}
	return nil
}

func (runner *Runner) RunSubscription(ctx context.Context, guildID, name string, options ManualRunOptions) error {
	if !guildAllowed(runner.allowedGuilds, guildID) {
		return ErrGuildNotAllowed
	}
	hoursOld := runner.config.HoursOld
	if options.HoursOld != nil {
		if *options.HoursOld < 0 {
			return errors.New("manual run hours_old must not be negative")
		}
		hoursOld = *options.HoursOld
	}
	subscription, err := runner.repository.GetSubscription(ctx, guildID, name)
	if err != nil {
		return err
	}
	// A manual run is allowed for a disabled subscription without enabling its
	// future scheduled runs.
	subscription.Enabled = true
	return runner.run(ctx, []Subscription{subscription}, hoursOld)
}

func (runner *Runner) run(ctx context.Context, subscriptions []Subscription, hoursOld int) error {
	if subscriptions == nil {
		var err error
		subscriptions, err = runner.repository.ListEnabledSubscriptions(ctx)
		if err != nil {
			return err
		}
	}
	searchCache := make(map[string]searchResult)
	candidates := make(map[string]*notificationCandidate)
	var runErrors []error
	for _, subscription := range subscriptions {
		if !subscription.Enabled || !guildAllowed(runner.allowedGuilds, subscription.GuildID) {
			continue
		}
		cacheKey := subscription.Query + "\x00" + strings.Join(subscription.Locations, "\x00")
		result, exists := searchCache[cacheKey]
		if !exists {
			searchCtx, cancel := context.WithTimeout(ctx, runner.config.RunTimeout)
			jobs, err := runner.searcher.Search(searchCtx, finder.SearchOptions{
				Query: subscription.Query, Locations: subscription.Locations,
				ResultsWanted: runner.config.ResultsWanted, HoursOld: hoursOld,
				FetchDescription: true, DescriptionFormat: finder.DescriptionPlain,
			})
			cancel()
			result = searchResult{jobs: jobs, err: err}
			searchCache[cacheKey] = result
		}
		if result.err != nil {
			runErrors = append(runErrors, fmt.Errorf("search %q: %w", subscription.Name, result.err))
		}
		for _, job := range result.jobs {
			if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.JobURL) == "" {
				continue
			}
			key := postedKey(job.DatePosted)
			evaluation := Evaluation{CacheKey: evaluationCacheKey(subscription, job)}
			decision, found, err := runner.repository.GetEvaluation(ctx, evaluation)
			if err != nil {
				runErrors = append(runErrors, fmt.Errorf("read evaluation for %s: %w", job.ID, err))
				continue
			}
			if !found {
				decision, err = runner.evaluator.Evaluate(ctx, subscription, job)
				if err != nil {
					runErrors = append(runErrors, fmt.Errorf("evaluate %s for %q: %w", job.ID, subscription.Name, err))
					continue
				}
				evaluation.Matched = decision.Match
				evaluation.Reason = decision.Reason
				evaluation.Overview = decision.Overview
				if err := runner.repository.SaveEvaluation(ctx, evaluation); err != nil {
					runErrors = append(runErrors, err)
					continue
				}
			}
			if !decision.Match {
				continue
			}
			candidateKey := subscription.ChannelID + "\x00" + job.ID + "\x00" + key
			candidate := candidates[candidateKey]
			if candidate == nil {
				candidate = &notificationCandidate{job: job, channelID: subscription.ChannelID, postedKey: key, overview: decision.Overview}
				candidates[candidateKey] = candidate
			}
		}
	}

	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		candidate := candidates[key]
		notification := Notification{
			ChannelID: candidate.channelID, LinkedInJobID: candidate.job.ID, PostedKey: candidate.postedKey,
			Title: candidate.job.Title, CompanyName: candidate.job.CompanyName, JobURL: candidate.job.JobURL,
			CompanyLogoURL: candidate.job.CompanyLogoURL, Location: normalizedLocation(candidate.job.Location),
			AIOverview: candidate.overview, DatePosted: candidate.job.DatePosted,
		}
		id, claimed, err := runner.repository.ClaimNotification(ctx, notification)
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		if !claimed {
			continue
		}
		notification.Reposted, err = runner.repository.WasPreviouslyNotified(
			ctx, notification.ChannelID, notification.LinkedInJobID, notification.PostedKey,
		)
		if err != nil {
			runErrors = append(runErrors, err)
			if releaseErr := runner.repository.ReleaseNotification(ctx, id); releaseErr != nil {
				runErrors = append(runErrors, releaseErr)
			}
			continue
		}
		err = runner.notifier.SendJob(ctx, notification)
		if err != nil {
			runErrors = append(runErrors, fmt.Errorf("notify job %s: %w", notification.LinkedInJobID, err))
			var deliveryError *DeliveryError
			if errors.As(err, &deliveryError) && deliveryError.SafeToRetry {
				if releaseErr := runner.repository.ReleaseNotification(ctx, id); releaseErr != nil {
					runErrors = append(runErrors, releaseErr)
				}
			}
			continue
		}
		if err := runner.repository.CompleteNotification(ctx, id); err != nil {
			runErrors = append(runErrors, err)
		}
	}
	return errors.Join(runErrors...)
}

type searchResult struct {
	jobs []finder.Job
	err  error
}

type notificationCandidate struct {
	job       finder.Job
	channelID string
	postedKey string
	overview  string
}

func RunScheduler(ctx context.Context, runner *Runner, interval, timeout time.Duration, logger *slog.Logger) {
	run := func() {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := runner.RunScheduled(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("scheduled job search finished with errors", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
