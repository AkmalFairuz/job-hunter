package finder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

const maxLinkedInOffset = 1000

// Search finds jobs through LinkedIn's public guest endpoints. If a later
// page or individual detail request fails, Search returns the jobs collected
// so far together with an error.
func (c *Client) Search(ctx context.Context, options SearchOptions) ([]Job, error) {
	options, err := normalizeSearchOptions(options)
	if err != nil {
		return nil, err
	}

	initialCapacity := min(options.ResultsWanted, maxLinkedInOffset)
	jobs := make([]Job, 0, initialCapacity)
	seenIDs := make(map[string]struct{}, initialCapacity)
	searchErrors := make([]error, 0)
	locations := requestedLocations(options)
	type locationState struct {
		name   string
		start  int
		active bool
	}
	states := make([]locationState, len(locations))
	activeLocations := len(locations)
	for index, location := range locations {
		states[index] = locationState{name: location, start: options.Offset / 10 * 10, active: true}
	}

	requestMade := false
	for len(jobs) < options.ResultsWanted && activeLocations > 0 {
		roundCards := make([][]searchCard, len(states))
		stopAfterRound := false
		for index := range states {
			state := &states[index]
			if !state.active {
				continue
			}
			if state.start >= maxLinkedInOffset {
				state.active = false
				activeLocations--
				continue
			}
			if requestMade {
				if err := c.sleep(ctx, c.pageDelay()); err != nil {
					wrapped := &SearchError{Operation: "pagination delay", Err: err}
					searchErrors = append(searchErrors, wrapped)
					stopAfterRound = true
					break
				}
			}
			requestMade = true

			searchURL := c.searchURL(options, state.name, state.start)
			resp, requestErr := c.get(ctx, searchURL, "search request", c.searchTimeout)
			if requestErr != nil {
				searchErrors = append(searchErrors, requestErr)
				state.active = false
				activeLocations--
				if ctx.Err() != nil {
					stopAfterRound = true
					break
				}
				continue
			}

			cards, pageSize, parseErr := parseSearchPage(resp.Body, c.baseURL)
			_ = resp.Body.Close()
			if parseErr != nil {
				searchErrors = append(searchErrors, &SearchError{Operation: "search response parsing", URL: searchURL, Err: parseErr})
				state.active = false
				activeLocations--
				continue
			}
			if pageSize == 0 {
				state.active = false
				activeLocations--
				continue
			}

			roundCards[index] = cards
			state.start += pageSize
		}
		jobs = appendRoundRobin(jobs, seenIDs, roundCards, options.ResultsWanted)
		if stopAfterRound {
			return jobs, joinErrors(searchErrors)
		}
	}

	if options.FetchDescription {
		for index := range jobs {
			detailErr := c.enrichJob(ctx, &jobs[index], options.DescriptionFormat)
			if detailErr != nil {
				searchErrors = append(searchErrors, detailErr)
				if ctx.Err() != nil {
					return jobs, joinErrors(searchErrors)
				}
			}
			jobs[index].IsRemote = isRemote(jobs[index].Title, jobs[index].Description, jobs[index].Location)
		}
	}

	return jobs, joinErrors(searchErrors)
}

func appendRoundRobin(jobs []Job, seenIDs map[string]struct{}, batches [][]searchCard, limit int) []Job {
	for cardIndex := 0; len(jobs) < limit; cardIndex++ {
		foundCard := false
		for _, cards := range batches {
			if cardIndex >= len(cards) {
				continue
			}
			foundCard = true
			job := cards[cardIndex].job
			if _, exists := seenIDs[job.ID]; exists {
				continue
			}
			seenIDs[job.ID] = struct{}{}
			jobs = append(jobs, job)
			if len(jobs) == limit {
				break
			}
		}
		if !foundCard {
			break
		}
	}
	return jobs
}

func normalizeSearchOptions(options SearchOptions) (SearchOptions, error) {
	options.Query = strings.TrimSpace(options.Query)
	if options.Distance < 0 {
		return options, fmt.Errorf("distance must not be negative")
	}
	if options.ResultsWanted < 0 {
		return options, fmt.Errorf("results wanted must not be negative")
	}
	if options.Offset < 0 {
		return options, fmt.Errorf("offset must not be negative")
	}
	if options.HoursOld < 0 {
		return options, fmt.Errorf("hours old must not be negative")
	}
	if int64(options.HoursOld) > math.MaxInt64/3600 {
		return options, fmt.Errorf("hours old is too large")
	}
	if options.Distance == 0 {
		options.Distance = 50
	}
	if options.ResultsWanted == 0 {
		options.ResultsWanted = 15
	}
	if options.DescriptionFormat == "" {
		options.DescriptionFormat = DescriptionMarkdown
	}
	if options.DescriptionFormat != DescriptionMarkdown &&
		options.DescriptionFormat != DescriptionHTML &&
		options.DescriptionFormat != DescriptionPlain {
		return options, fmt.Errorf("unsupported description format %q", options.DescriptionFormat)
	}
	if options.JobType != "" && jobTypeCode(options.JobType) == "" {
		return options, fmt.Errorf("unsupported job type %q", options.JobType)
	}
	return options, nil
}

func requestedLocations(options SearchOptions) []string {
	values := options.Locations
	if len(values) == 0 {
		return []string{""}
	}
	locations := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		location := strings.TrimSpace(value)
		if location == "" && len(values) > 1 {
			continue
		}
		key := strings.ToLower(location)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		locations = append(locations, location)
	}
	if len(locations) == 0 {
		return []string{""}
	}
	return locations
}

func (c *Client) searchURL(options SearchOptions, location string, start int) string {
	query := make(url.Values)
	if options.Query != "" {
		query.Set("keywords", options.Query)
	}
	if location != "" {
		query.Set("location", location)
	}
	query.Set("distance", strconv.Itoa(options.Distance))
	query.Set("pageNum", "0")
	query.Set("start", strconv.Itoa(start))
	if options.RemoteOnly {
		query.Set("f_WT", "2")
	}
	if code := jobTypeCode(options.JobType); code != "" {
		query.Set("f_JT", code)
	}
	if options.EasyApplyOnly {
		query.Set("f_AL", "true")
	}
	if options.HoursOld > 0 {
		query.Set("f_TPR", "r"+strconv.FormatInt(int64(options.HoursOld)*3600, 10))
	}
	if len(options.CompanyIDs) > 0 {
		companyIDs := make([]string, len(options.CompanyIDs))
		for index, id := range options.CompanyIDs {
			companyIDs[index] = strconv.FormatInt(id, 10)
		}
		query.Set("f_C", strings.Join(companyIDs, ","))
	}
	return strings.TrimRight(c.baseURL, "/") + "/jobs-guest/jobs/api/seeMoreJobPostings/search?" + query.Encode()
}

func (c *Client) enrichJob(ctx context.Context, job *Job, format DescriptionFormat) error {
	detailURL := strings.TrimRight(c.baseURL, "/") + "/jobs/view/" + url.PathEscape(strings.TrimPrefix(job.ID, "li-"))
	resp, err := c.get(ctx, detailURL, "job detail request", c.detailTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.Request != nil && resp.Request.URL != nil && strings.Contains(resp.Request.URL.String(), "linkedin.com/signup") {
		return &SearchError{Operation: "job detail request", URL: detailURL, Err: errors.New("redirected to LinkedIn signup")}
	}
	details, err := parseJobDetails(resp.Body, format)
	if err != nil {
		return &SearchError{Operation: "job detail parsing", URL: detailURL, Err: err}
	}

	job.Description = details.description
	job.JobTypes = details.jobTypes
	job.JobLevel = strings.ToLower(details.jobLevel)
	job.CompanyIndustry = details.companyIndustry
	job.JobURLDirect = details.jobURLDirect
	if details.companyLogoURL != "" {
		job.CompanyLogoURL = details.companyLogoURL
	}
	if job.Location.String() == "" && details.location.String() != "" {
		job.Location = details.location
	}
	job.JobFunction = details.jobFunction
	job.Emails = extractEmails(details.description)
	return nil
}

func jobTypeCode(jobType JobType) string {
	switch jobType {
	case JobTypeFullTime:
		return "F"
	case JobTypePartTime:
		return "P"
	case JobTypeInternship:
		return "I"
	case JobTypeContract:
		return "C"
	case JobTypeTemporary:
		return "T"
	default:
		return ""
	}
}

func joinErrors(existing []error, additional ...error) error {
	all := make([]error, 0, len(existing)+len(additional))
	for _, err := range existing {
		if err != nil {
			all = append(all, err)
		}
	}
	for _, err := range additional {
		if err != nil {
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}
