package finder

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// JobType is a normalized LinkedIn employment type.
type JobType string

const (
	JobTypeFullTime   JobType = "fulltime"
	JobTypePartTime   JobType = "parttime"
	JobTypeContract   JobType = "contract"
	JobTypeTemporary  JobType = "temporary"
	JobTypeInternship JobType = "internship"
	JobTypePerDiem    JobType = "perdiem"
	JobTypeNights     JobType = "nights"
	JobTypeOther      JobType = "other"
	JobTypeSummer     JobType = "summer"
	JobTypeVolunteer  JobType = "volunteer"
)

// DescriptionFormat controls the representation of an enriched job
// description.
type DescriptionFormat string

const (
	DescriptionMarkdown DescriptionFormat = "markdown"
	DescriptionHTML     DescriptionFormat = "html"
	DescriptionPlain    DescriptionFormat = "plain"
)

// CompensationInterval describes the period covered by compensation.
// LinkedIn's search cards do not always expose this value.
type CompensationInterval string

const (
	CompensationYearly  CompensationInterval = "yearly"
	CompensationMonthly CompensationInterval = "monthly"
	CompensationWeekly  CompensationInterval = "weekly"
	CompensationDaily   CompensationInterval = "daily"
	CompensationHourly  CompensationInterval = "hourly"
)

// ClientConfig configures a Client. Zero values select JobSpy-compatible
// defaults.
type ClientConfig struct {
	// HTTPClient may configure proxies, custom CAs, TLS, or cookie behavior.
	// Finder does not mutate it. A nil value uses a private default client.
	HTTPClient *http.Client
	// UserAgent overrides the browser user agent sent to LinkedIn.
	UserAgent string
}

// SearchOptions contains LinkedIn search and enrichment filters.
type SearchOptions struct {
	// Query is the LinkedIn job search text.
	Query string
	// Locations searches several LinkedIn locations and merges the results.
	// An empty slice applies no location filter. Duplicate and blank values are
	// ignored.
	Locations         []string
	Distance          int
	RemoteOnly        bool
	JobType           JobType
	EasyApplyOnly     bool
	ResultsWanted     int
	Offset            int
	HoursOld          int
	CompanyIDs        []int64
	FetchDescription  bool
	DescriptionFormat DescriptionFormat
}

// Location is the structured location parsed from a LinkedIn search card.
type Location struct {
	City    string `json:"city,omitempty"`
	State   string `json:"state,omitempty"`
	Country string `json:"country,omitempty"`
}

// String formats the non-empty parts of a location. The internal
// "worldwide" fallback is intentionally omitted.
func (l Location) String() string {
	parts := make([]string, 0, 3)
	if l.City != "" {
		parts = append(parts, l.City)
	}
	if l.State != "" {
		parts = append(parts, l.State)
	}
	if l.Country != "" && !strings.EqualFold(l.Country, "worldwide") {
		parts = append(parts, l.Country)
	}
	return strings.Join(parts, ", ")
}

// Compensation is salary data directly exposed by LinkedIn.
type Compensation struct {
	Interval  CompensationInterval `json:"interval,omitempty"`
	MinAmount float64              `json:"min_amount,omitempty"`
	MaxAmount float64              `json:"max_amount,omitempty"`
	Currency  string               `json:"currency,omitempty"`
}

// Job is a LinkedIn job listing. Fields populated from a detail page remain
// empty unless SearchOptions.FetchDescription is true.
type Job struct {
	ID              string        `json:"id,omitempty"`
	Title           string        `json:"title"`
	CompanyName     string        `json:"company_name,omitempty"`
	CompanyURL      string        `json:"company_url,omitempty"`
	Location        Location      `json:"location"`
	IsRemote        bool          `json:"is_remote"`
	DatePosted      *time.Time    `json:"date_posted,omitempty"`
	JobURL          string        `json:"job_url"`
	JobURLDirect    string        `json:"job_url_direct,omitempty"`
	Compensation    *Compensation `json:"compensation,omitempty"`
	JobTypes        []JobType     `json:"job_types,omitempty"`
	JobLevel        string        `json:"job_level,omitempty"`
	CompanyIndustry string        `json:"company_industry,omitempty"`
	Description     string        `json:"description,omitempty"`
	Emails          []string      `json:"emails,omitempty"`
	CompanyLogoURL  string        `json:"company_logo_url,omitempty"`
	JobFunction     string        `json:"job_function,omitempty"`
}

// Searcher is implemented by LinkedIn job finders.
type Searcher interface {
	Search(context.Context, SearchOptions) ([]Job, error)
}
