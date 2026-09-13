package jobbot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/akmalfairuz/job-hunter/finder"
)

var ErrGuildNotAllowed = errors.New("Discord guild is not allowed")

func guildSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func guildAllowed(allowed map[string]struct{}, guildID string) bool {
	_, exists := allowed[guildID]
	return exists
}

type Subscription struct {
	ID        int64
	GuildID   string
	ChannelID string
	Name      string
	Query     string
	Locations []string
	AIPrompt  string
	Enabled   bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SubscriptionInput struct {
	GuildID   string
	ChannelID string
	Name      string
	Query     string
	Locations []string
	AIPrompt  string
	CreatedBy string
}

type Evaluation struct {
	CacheKey string
	Matched  bool
	Reason   string
	Overview string
}

type Notification struct {
	ChannelID      string
	LinkedInJobID  string
	PostedKey      string
	Title          string
	CompanyName    string
	JobURL         string
	CompanyLogoURL string
	Location       string
	AIOverview     string
	DatePosted     *time.Time
	Reposted       bool
}

type MatchDecision struct {
	Match    bool   `json:"match"`
	Reason   string `json:"reason"`
	Overview string `json:"overview"`
}

func normalizeLocations(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func parseLocations(value string) []string {
	return normalizeLocations(strings.Split(value, ","))
}

func normalizedLocation(location finder.Location) string {
	parts := normalizeLocations([]string{location.City, location.State, location.Country})
	withoutWorldwide := make([]string, 0, len(parts))
	for _, part := range parts {
		if !strings.EqualFold(part, "worldwide") {
			withoutWorldwide = append(withoutWorldwide, part)
		}
	}
	if len(withoutWorldwide) > 0 {
		return strings.Join(withoutWorldwide, ", ")
	}
	if len(parts) > 0 {
		return "Global"
	}
	return ""
}

func postedKey(posted *time.Time) string {
	if posted == nil {
		return "unknown"
	}
	return posted.UTC().Format("2006-01-02")
}

func jobContentHash(job finder.Job) string {
	value := strings.Join([]string{
		normalizeCacheText(job.Title),
		normalizeCacheText(job.CompanyName),
		normalizeCacheText(normalizedLocation(job.Location)),
		normalizeCacheText(job.Description),
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Bump this when evaluator instructions change enough to invalidate cached
// decisions or overviews.
const evaluationCacheVersion = "job-filter-v2"

// evaluationCacheKey deliberately excludes LinkedIn job ID and posted date.
// An unchanged repost should be notified again but should not spend LLM tokens
// on the same filtering decision.
func evaluationCacheKey(subscription Subscription, job finder.Job) string {
	locations := normalizeLocations(subscription.Locations)
	for index := range locations {
		locations[index] = strings.ToLower(normalizeCacheText(locations[index]))
	}
	sort.Strings(locations)
	value := strings.Join([]string{
		evaluationCacheVersion,
		strings.ToLower(normalizeCacheText(subscription.Query)),
		strings.Join(locations, "\x1f"),
		normalizeCacheText(subscription.AIPrompt),
		jobContentHash(job),
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeCacheText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func marshalStrings(values []string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}
