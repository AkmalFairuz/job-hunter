// Package jobbot implements the LinkedIn vacancy Discord bot.
package jobbot

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	env "github.com/caarlos0/env/v11"
	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

// Config is the centralized runtime configuration for jobbot.
type Config struct {
	Discord   DiscordConfig   `envPrefix:"DISCORD_"`
	Database  DatabaseConfig  `envPrefix:"MYSQL_"`
	LLM       LLMConfig       `envPrefix:"LLM_"`
	Scheduler SchedulerConfig `envPrefix:"SCHEDULER_"`
	LinkedIn  LinkedInConfig  `envPrefix:"LINKEDIN_"`
}

type DiscordConfig struct {
	Token           string        `env:"TOKEN,required"`
	ApplicationID   string        `env:"APPLICATION_ID,required"`
	AllowedGuildIDs []string      `env:"ALLOWED_GUILD_IDS,required"`
	HTTPTimeout     time.Duration `env:"HTTP_TIMEOUT" envDefault:"15s"`
}

type DatabaseConfig struct {
	Host            string        `env:"HOST" envDefault:"mysql"`
	Port            uint16        `env:"PORT" envDefault:"3306"`
	User            string        `env:"USER" envDefault:"jobbot"`
	Password        string        `env:"PASSWORD,required"`
	Database        string        `env:"DATABASE" envDefault:"jobbot"`
	MaxOpenConns    int           `env:"MAX_OPEN_CONNS" envDefault:"10"`
	MaxIdleConns    int           `env:"MAX_IDLE_CONNS" envDefault:"10"`
	ConnMaxLifetime time.Duration `env:"CONN_MAX_LIFETIME" envDefault:"3m"`
}

// DSN derives the MySQL connection string from the individual database settings.
func (config DatabaseConfig) DSN() string {
	return (&mysql.Config{
		User:      strings.TrimSpace(config.User),
		Passwd:    config.Password,
		Net:       "tcp",
		Addr:      net.JoinHostPort(strings.TrimSpace(config.Host), strconv.FormatUint(uint64(config.Port), 10)),
		DBName:    strings.TrimSpace(config.Database),
		ParseTime: true,
		Loc:       time.UTC,
	}).FormatDSN()
}

type LLMConfig struct {
	BaseURL             string        `env:"BASE_URL,required"`
	APIKey              string        `env:"API_KEY,required"`
	Model               string        `env:"MODEL" envDefault:"gpt-5.6-luna"`
	ReasoningEffort     string        `env:"REASONING_EFFORT" envDefault:"medium"`
	Timeout             time.Duration `env:"TIMEOUT" envDefault:"30s"`
	MaxDescriptionRunes int           `env:"MAX_DESCRIPTION_RUNES" envDefault:"12000"`
}

type SchedulerConfig struct {
	Interval      time.Duration `env:"INTERVAL" envDefault:"5m"`
	ResultsWanted int           `env:"RESULTS_WANTED" envDefault:"50"`
	HoursOld      int           `env:"HOURS_OLD" envDefault:"24"`
	RunTimeout    time.Duration `env:"RUN_TIMEOUT" envDefault:"10m"`
}

type LinkedInConfig struct {
	UserAgent string `env:"USER_AGENT"`
}

// LoadConfig loads .env when it exists and then parses the process environment.
// Existing process variables take precedence over values in .env.
func LoadConfig() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	var config Config
	if err := env.Parse(&config); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	allowedGuildIDs, err := normalizeGuildIDs(config.Discord.AllowedGuildIDs)
	if err != nil {
		return Config{}, err
	}
	config.Discord.AllowedGuildIDs = allowedGuildIDs
	config.LLM.ReasoningEffort = strings.ToLower(strings.TrimSpace(config.LLM.ReasoningEffort))
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if _, err := normalizeGuildIDs(config.Discord.AllowedGuildIDs); err != nil {
		return err
	}
	switch {
	case config.Discord.HTTPTimeout <= 0:
		return errors.New("DISCORD_HTTP_TIMEOUT must be greater than zero")
	case strings.TrimSpace(config.Database.Host) == "":
		return errors.New("MYSQL_HOST must not be empty")
	case config.Database.Port == 0:
		return errors.New("MYSQL_PORT must be greater than zero")
	case strings.TrimSpace(config.Database.User) == "":
		return errors.New("MYSQL_USER must not be empty")
	case strings.TrimSpace(config.Database.Database) == "":
		return errors.New("MYSQL_DATABASE must not be empty")
	case config.Database.MaxOpenConns < 2:
		return errors.New("MYSQL_MAX_OPEN_CONNS must be at least two because the scheduler holds a dedicated lock connection")
	case config.Database.MaxIdleConns < 0:
		return errors.New("MYSQL_MAX_IDLE_CONNS must not be negative")
	case config.Database.ConnMaxLifetime <= 0:
		return errors.New("MYSQL_CONN_MAX_LIFETIME must be greater than zero")
	case config.LLM.Timeout <= 0:
		return errors.New("LLM_TIMEOUT must be greater than zero")
	case config.LLM.MaxDescriptionRunes <= 0:
		return errors.New("LLM_MAX_DESCRIPTION_RUNES must be greater than zero")
	case !validReasoningEffort(config.LLM.ReasoningEffort):
		return errors.New("LLM_REASONING_EFFORT must be one of: none, low, medium, high, xhigh, max")
	case config.Scheduler.Interval <= 0:
		return errors.New("SCHEDULER_INTERVAL must be greater than zero")
	case config.Scheduler.ResultsWanted <= 0:
		return errors.New("SCHEDULER_RESULTS_WANTED must be greater than zero")
	case config.Scheduler.HoursOld < 0:
		return errors.New("SCHEDULER_HOURS_OLD must not be negative")
	case config.Scheduler.RunTimeout <= 0:
		return errors.New("SCHEDULER_RUN_TIMEOUT must be greater than zero")
	}
	if _, err := openAIBaseURL(config.LLM.BaseURL); err != nil {
		return fmt.Errorf("LLM_BASE_URL: %w", err)
	}
	return nil
}

func validReasoningEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func normalizeGuildIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("DISCORD_ALLOWED_GUILD_IDS contains invalid guild ID %q", value)
		}
		value = strconv.FormatUint(id, 10)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, errors.New("DISCORD_ALLOWED_GUILD_IDS must contain at least one guild ID")
	}
	return result, nil
}
