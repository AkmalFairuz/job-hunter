package jobbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("not found")

type Repository interface {
	CreateSubscription(context.Context, SubscriptionInput) (Subscription, error)
	UpdateSubscription(context.Context, Subscription) error
	DeleteSubscription(context.Context, string, string) error
	SetSubscriptionEnabled(context.Context, string, string, bool) error
	GetSubscription(context.Context, string, string) (Subscription, error)
	ListSubscriptions(context.Context, string, string) ([]Subscription, error)
	ListEnabledSubscriptions(context.Context) ([]Subscription, error)
	GetEvaluation(context.Context, Evaluation) (MatchDecision, bool, error)
	SaveEvaluation(context.Context, Evaluation) error
	ClaimNotification(context.Context, Notification) (int64, bool, error)
	WasPreviouslyNotified(context.Context, string, string, string) (bool, error)
	CompleteNotification(context.Context, int64) error
	ReleaseNotification(context.Context, int64) error
	WithSchedulerLock(context.Context, func(context.Context) error) (bool, error)
}

type Store struct {
	db *sqlx.DB
}

func OpenStore(ctx context.Context, config DatabaseConfig) (*Store, error) {
	dsn, err := mysql.ParseDSN(config.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DSN: %w", err)
	}
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	db, err := sqlx.Open("mysql", dsn.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open MySQL: %w", err)
	}
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping MySQL: %w", err)
	}
	store := &Store{db: db}
	if err := store.checkSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) checkSchema(ctx context.Context) error {
	for _, table := range []string{"job_subscriptions", "job_evaluations", "job_notifications"} {
		var count int
		if err := store.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`, table); err != nil {
			return fmt.Errorf("check schema: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("required table %q is missing; apply migrations/001_jobbot.sql", table)
		}
	}
	return nil
}

type subscriptionRow struct {
	ID            int64     `db:"id"`
	GuildID       string    `db:"guild_id"`
	ChannelID     string    `db:"channel_id"`
	Name          string    `db:"name"`
	Query         string    `db:"search_query"`
	LocationsJSON []byte    `db:"locations_json"`
	AIPrompt      string    `db:"ai_prompt"`
	Enabled       bool      `db:"enabled"`
	CreatedBy     string    `db:"created_by"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

const subscriptionColumns = `id, guild_id, channel_id, name, search_query, locations_json, ai_prompt, enabled, created_by, created_at, updated_at`

func (row subscriptionRow) subscription() (Subscription, error) {
	var locations []string
	if err := json.Unmarshal(row.LocationsJSON, &locations); err != nil {
		return Subscription{}, fmt.Errorf("decode subscription locations: %w", err)
	}
	return Subscription{
		ID: row.ID, GuildID: row.GuildID, ChannelID: row.ChannelID, Name: row.Name,
		Query: row.Query, Locations: locations, AIPrompt: row.AIPrompt,
		Enabled: row.Enabled, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (store *Store) CreateSubscription(ctx context.Context, input SubscriptionInput) (Subscription, error) {
	locations := marshalStrings(normalizeLocations(input.Locations))
	result, err := store.db.ExecContext(ctx, `INSERT INTO job_subscriptions
		(guild_id, channel_id, name, search_query, locations_json, ai_prompt, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, input.GuildID, input.ChannelID, input.Name, input.Query, locations, input.AIPrompt, input.CreatedBy)
	if err != nil {
		return Subscription{}, fmt.Errorf("create subscription: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Subscription{}, fmt.Errorf("read subscription ID: %w", err)
	}
	var row subscriptionRow
	if err := store.db.GetContext(ctx, &row, `SELECT `+subscriptionColumns+` FROM job_subscriptions WHERE id = ?`, id); err != nil {
		return Subscription{}, fmt.Errorf("read created subscription: %w", err)
	}
	return row.subscription()
}

func (store *Store) UpdateSubscription(ctx context.Context, subscription Subscription) error {
	result, err := store.db.ExecContext(ctx, `UPDATE job_subscriptions SET channel_id = ?, search_query = ?, locations_json = ?, ai_prompt = ?
		WHERE guild_id = ? AND name = ?`, subscription.ChannelID, subscription.Query, marshalStrings(normalizeLocations(subscription.Locations)), subscription.AIPrompt, subscription.GuildID, subscription.Name)
	if err != nil {
		return fmt.Errorf("update subscription: %w", err)
	}
	return requireChanged(result)
}

func (store *Store) DeleteSubscription(ctx context.Context, guildID, name string) error {
	result, err := store.db.ExecContext(ctx, `DELETE FROM job_subscriptions WHERE guild_id = ? AND name = ?`, guildID, name)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return requireChanged(result)
}

func (store *Store) SetSubscriptionEnabled(ctx context.Context, guildID, name string, enabled bool) error {
	result, err := store.db.ExecContext(ctx, `UPDATE job_subscriptions SET enabled = ? WHERE guild_id = ? AND name = ?`, enabled, guildID, name)
	if err != nil {
		return fmt.Errorf("set subscription enabled: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err = store.GetSubscription(ctx, guildID, name)
	return err
}

func requireChanged(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (store *Store) GetSubscription(ctx context.Context, guildID, name string) (Subscription, error) {
	var row subscriptionRow
	err := store.db.GetContext(ctx, &row, `SELECT `+subscriptionColumns+` FROM job_subscriptions WHERE guild_id = ? AND name = ?`, guildID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("get subscription: %w", err)
	}
	return row.subscription()
}

func (store *Store) ListSubscriptions(ctx context.Context, guildID, channelID string) ([]Subscription, error) {
	query := `SELECT ` + subscriptionColumns + ` FROM job_subscriptions WHERE guild_id = ?`
	args := []any{guildID}
	if channelID != "" {
		query += ` AND channel_id = ?`
		args = append(args, channelID)
	}
	query += ` ORDER BY name`
	return store.listSubscriptions(ctx, query, args...)
}

func (store *Store) ListEnabledSubscriptions(ctx context.Context) ([]Subscription, error) {
	return store.listSubscriptions(ctx, `SELECT `+subscriptionColumns+` FROM job_subscriptions WHERE enabled = TRUE ORDER BY id`)
}

func (store *Store) listSubscriptions(ctx context.Context, query string, args ...any) ([]Subscription, error) {
	var rows []subscriptionRow
	if err := store.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	result := make([]Subscription, 0, len(rows))
	for _, row := range rows {
		subscription, err := row.subscription()
		if err != nil {
			return nil, err
		}
		result = append(result, subscription)
	}
	return result, nil
}

func (store *Store) GetEvaluation(ctx context.Context, evaluation Evaluation) (MatchDecision, bool, error) {
	var result struct {
		Matched  bool   `db:"matched"`
		Reason   string `db:"reason"`
		Overview string `db:"overview"`
	}
	err := store.db.GetContext(ctx, &result, `SELECT matched, reason, overview FROM job_evaluations
		WHERE cache_key = ?`, evaluation.CacheKey)
	if errors.Is(err, sql.ErrNoRows) {
		return MatchDecision{}, false, nil
	}
	if err != nil {
		return MatchDecision{}, false, fmt.Errorf("get evaluation: %w", err)
	}
	return MatchDecision{Match: result.Matched, Reason: result.Reason, Overview: result.Overview}, true, nil
}

func (store *Store) SaveEvaluation(ctx context.Context, evaluation Evaluation) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO job_evaluations
		(cache_key, matched, reason, overview)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE matched = VALUES(matched), reason = VALUES(reason), overview = VALUES(overview), evaluated_at = CURRENT_TIMESTAMP`,
		evaluation.CacheKey, evaluation.Matched, truncateRunes(evaluation.Reason, 2000), truncateRunes(evaluation.Overview, 1000))
	if err != nil {
		return fmt.Errorf("save evaluation: %w", err)
	}
	return nil
}

func (store *Store) ClaimNotification(ctx context.Context, notification Notification) (int64, bool, error) {
	result, err := store.db.ExecContext(ctx, `INSERT IGNORE INTO job_notifications
		(channel_id, linkedin_job_id, posted_key, status)
		VALUES (?, ?, ?, 'pending')`, notification.ChannelID, notification.LinkedInJobID, notification.PostedKey)
	if err != nil {
		return 0, false, fmt.Errorf("claim notification: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("claim notification result: %w", err)
	}
	if rows == 0 {
		return 0, false, nil
	}
	id, err := result.LastInsertId()
	return id, true, err
}

func (store *Store) WasPreviouslyNotified(ctx context.Context, channelID, linkedInJobID, postedKey string) (bool, error) {
	var previouslyNotified bool
	err := store.db.GetContext(ctx, &previouslyNotified, `SELECT EXISTS(
		SELECT 1 FROM job_notifications
		WHERE channel_id = ? AND linkedin_job_id = ? AND posted_key <> ? AND status = 'sent'
	)`, channelID, linkedInJobID, postedKey)
	if err != nil {
		return false, fmt.Errorf("check previous notification: %w", err)
	}
	return previouslyNotified, nil
}

func (store *Store) CompleteNotification(ctx context.Context, id int64) error {
	_, err := store.db.ExecContext(ctx, `UPDATE job_notifications SET status = 'sent', sent_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("complete notification: %w", err)
	}
	return nil
}

func (store *Store) ReleaseNotification(ctx context.Context, id int64) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM job_notifications WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("release notification: %w", err)
	}
	return nil
}

func (store *Store) WithSchedulerLock(ctx context.Context, run func(context.Context) error) (bool, error) {
	connection, err := store.db.Connx(ctx)
	if err != nil {
		return false, fmt.Errorf("get scheduler lock connection: %w", err)
	}
	defer connection.Close()
	var acquired sql.NullInt64
	if err := connection.GetContext(ctx, &acquired, `SELECT GET_LOCK('jobbot:scheduler', 0)`); err != nil {
		return false, fmt.Errorf("acquire scheduler lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return false, nil
	}
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		_ = connection.GetContext(releaseContext, &released, `SELECT RELEASE_LOCK('jobbot:scheduler')`)
	}()
	return true, run(ctx)
}

func validateSubscription(input SubscriptionInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Query = strings.TrimSpace(input.Query)
	if input.GuildID == "" || input.ChannelID == "" || input.CreatedBy == "" {
		return errors.New("guild, channel, and creator are required")
	}
	if input.Name == "" || len(input.Name) > 100 {
		return errors.New("name must contain 1-100 characters")
	}
	if input.Query == "" {
		return errors.New("query is required")
	}
	if len(normalizeLocations(input.Locations)) == 0 {
		return errors.New("at least one location is required")
	}
	return nil
}
