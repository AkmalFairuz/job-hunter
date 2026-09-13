// Package cli implements the jobfinder command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/akmalfairuz/job-hunter/finder"
)

const defaultTimeout = 10 * time.Minute

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ", ") }

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

type int64ListFlag []int64

func (values *int64ListFlag) String() string {
	formatted := make([]string, len(*values))
	for index, value := range *values {
		formatted[index] = strconv.FormatInt(value, 10)
	}
	return strings.Join(formatted, ",")
}

func (values *int64ListFlag) Set(value string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid company ID %q: %w", value, err)
	}
	if id <= 0 {
		return errors.New("company ID must be greater than zero")
	}
	*values = append(*values, id)
	return nil
}

type config struct {
	query             string
	locations         []string
	distance          int
	remoteOnly        bool
	jobType           string
	easyApplyOnly     bool
	resultsWanted     int
	offset            int
	hoursOld          int
	companyIDs        []int64
	fetchDescription  bool
	descriptionFormat string
	userAgent         string
	outputFormat      string
	outputPath        string
	pretty            bool
	timeout           time.Duration
}

type searchMetadata struct {
	Query             string                   `json:"query"`
	Locations         []string                 `json:"locations,omitempty"`
	Distance          int                      `json:"distance"`
	RemoteOnly        bool                     `json:"remote_only"`
	JobType           finder.JobType           `json:"job_type,omitempty"`
	EasyApplyOnly     bool                     `json:"easy_apply_only"`
	ResultsWanted     int                      `json:"results_wanted"`
	Offset            int                      `json:"offset"`
	HoursOld          int                      `json:"hours_old"`
	CompanyIDs        []int64                  `json:"company_ids,omitempty"`
	FetchDescription  bool                     `json:"fetch_description"`
	DescriptionFormat finder.DescriptionFormat `json:"description_format"`
}

type searchOutput struct {
	Search    searchMetadata `json:"search"`
	FetchedAt time.Time      `json:"fetched_at"`
	Count     int            `json:"count"`
	Error     string         `json:"error,omitempty"`
	Jobs      []finder.Job   `json:"jobs"`
}

type clientFactory func(finder.ClientConfig) finder.Searcher

// Run executes the jobfinder CLI and returns its process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, stderr, func(clientConfig finder.ClientConfig) finder.Searcher {
		return finder.NewClient(clientConfig)
	})
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, newClient clientFactory) int {
	cliConfig, err := parseArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "jobfinder: %v\n", err)
		return 2
	}

	searchCtx, cancel := context.WithTimeout(ctx, cliConfig.timeout)
	defer cancel()
	searchOptions := finder.SearchOptions{
		Query:             cliConfig.query,
		Locations:         cliConfig.locations,
		Distance:          cliConfig.distance,
		RemoteOnly:        cliConfig.remoteOnly,
		JobType:           finder.JobType(cliConfig.jobType),
		EasyApplyOnly:     cliConfig.easyApplyOnly,
		ResultsWanted:     cliConfig.resultsWanted,
		Offset:            cliConfig.offset,
		HoursOld:          cliConfig.hoursOld,
		CompanyIDs:        cliConfig.companyIDs,
		FetchDescription:  cliConfig.fetchDescription,
		DescriptionFormat: finder.DescriptionFormat(cliConfig.descriptionFormat),
	}
	jobs, searchErr := newClient(finder.ClientConfig{
		HTTPClient: &http.Client{},
		UserAgent:  cliConfig.userAgent,
	}).Search(searchCtx, searchOptions)
	result := searchOutput{
		Search: searchMetadata{
			Query:             searchOptions.Query,
			Locations:         searchOptions.Locations,
			Distance:          searchOptions.Distance,
			RemoteOnly:        searchOptions.RemoteOnly,
			JobType:           searchOptions.JobType,
			EasyApplyOnly:     searchOptions.EasyApplyOnly,
			ResultsWanted:     searchOptions.ResultsWanted,
			Offset:            searchOptions.Offset,
			HoursOld:          searchOptions.HoursOld,
			CompanyIDs:        searchOptions.CompanyIDs,
			FetchDescription:  searchOptions.FetchDescription,
			DescriptionFormat: searchOptions.DescriptionFormat,
		},
		FetchedAt: time.Now(),
		Count:     len(jobs),
		Jobs:      jobs,
	}
	if searchErr != nil {
		result.Error = searchErr.Error()
	}

	output := stdout
	var file *os.File
	if cliConfig.outputPath != "" && cliConfig.outputPath != "-" {
		file, err = os.Create(cliConfig.outputPath)
		if err != nil {
			fmt.Fprintf(stderr, "jobfinder: create output: %v\n", err)
			return 1
		}
		output = file
	}
	writeErr := writeSearchOutput(output, result, cliConfig.outputFormat, cliConfig.pretty)
	if file != nil {
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
	}
	if writeErr != nil {
		fmt.Fprintf(stderr, "jobfinder: write output: %v\n", writeErr)
		return 1
	}
	if file != nil {
		fmt.Fprintf(stderr, "saved %d jobs to %s\n", len(jobs), cliConfig.outputPath)
	}
	if searchErr != nil {
		fmt.Fprintf(stderr, "jobfinder: search incomplete: %v\n", searchErr)
		return 1
	}
	return 0
}

func parseArgs(args []string, stderr io.Writer) (config, error) {
	cliConfig := config{}
	var locations stringListFlag
	var companyIDs int64ListFlag
	flags := flag.NewFlagSet("jobfinder", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cliConfig.query, "query", "", "LinkedIn job search query (required)")
	flags.Var(&locations, "location", "LinkedIn location; repeat for multiple locations")
	flags.IntVar(&cliConfig.distance, "distance", 50, "search radius in miles")
	flags.BoolVar(&cliConfig.remoteOnly, "remote", false, "return remote jobs only")
	flags.StringVar(&cliConfig.jobType, "job-type", "", "fulltime, parttime, internship, contract, or temporary")
	flags.BoolVar(&cliConfig.easyApplyOnly, "easy-apply", false, "request LinkedIn Easy Apply jobs only")
	flags.IntVar(&cliConfig.resultsWanted, "results", 15, "maximum total jobs to return")
	flags.IntVar(&cliConfig.offset, "offset", 0, "result offset for each location")
	flags.IntVar(&cliConfig.hoursOld, "hours-old", 0, "only jobs posted within this many hours")
	flags.Var(&companyIDs, "company-id", "LinkedIn company ID; repeat for multiple companies")
	flags.BoolVar(&cliConfig.fetchDescription, "fetch-description", false, "fetch job descriptions and detail fields")
	flags.StringVar(&cliConfig.descriptionFormat, "description-format", string(finder.DescriptionMarkdown), "markdown, html, or plain")
	flags.StringVar(&cliConfig.userAgent, "user-agent", "", "custom HTTP user agent")
	flags.StringVar(&cliConfig.outputFormat, "output", outputTable, "output format: table, wide, or json")
	flags.StringVar(&cliConfig.outputFormat, "o", outputTable, "output format: table, wide, or json (shorthand)")
	flags.StringVar(&cliConfig.outputPath, "output-file", "", "output file; defaults to stdout, use - for stdout")
	flags.BoolVar(&cliConfig.pretty, "pretty", true, "pretty-print JSON output")
	flags.DurationVar(&cliConfig.timeout, "timeout", defaultTimeout, "overall search timeout")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: jobfinder --query QUERY [options]")
		fmt.Fprintln(stderr, "\nSearch LinkedIn public jobs and print a table or JSON.")
		fmt.Fprintln(stderr, "\nOptions:")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return cliConfig, err
	}
	if flags.NArg() != 0 {
		return cliConfig, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	cliConfig.query = strings.TrimSpace(cliConfig.query)
	if cliConfig.query == "" {
		return cliConfig, errors.New("--query is required")
	}
	cliConfig.jobType = strings.ToLower(strings.TrimSpace(cliConfig.jobType))
	cliConfig.descriptionFormat = strings.ToLower(strings.TrimSpace(cliConfig.descriptionFormat))
	cliConfig.outputFormat = strings.ToLower(strings.TrimSpace(cliConfig.outputFormat))
	if cliConfig.distance < 0 {
		return cliConfig, errors.New("--distance must not be negative")
	}
	if cliConfig.resultsWanted <= 0 {
		return cliConfig, errors.New("--results must be greater than zero")
	}
	if cliConfig.offset < 0 {
		return cliConfig, errors.New("--offset must not be negative")
	}
	if cliConfig.hoursOld < 0 {
		return cliConfig, errors.New("--hours-old must not be negative")
	}
	if cliConfig.timeout <= 0 {
		return cliConfig, errors.New("--timeout must be greater than zero")
	}
	if !validJobType(cliConfig.jobType) {
		return cliConfig, fmt.Errorf("invalid --job-type %q", cliConfig.jobType)
	}
	if !validDescriptionFormat(cliConfig.descriptionFormat) {
		return cliConfig, fmt.Errorf("invalid --description-format %q", cliConfig.descriptionFormat)
	}
	if !validOutputFormat(cliConfig.outputFormat) {
		return cliConfig, fmt.Errorf("invalid --output %q: must be table, wide, or json", cliConfig.outputFormat)
	}
	cliConfig.locations = append([]string(nil), locations...)
	cliConfig.companyIDs = append([]int64(nil), companyIDs...)
	return cliConfig, nil
}

func validOutputFormat(value string) bool {
	switch value {
	case outputTable, outputWide, outputJSON:
		return true
	default:
		return false
	}
}

func validJobType(value string) bool {
	switch finder.JobType(value) {
	case "", finder.JobTypeFullTime, finder.JobTypePartTime, finder.JobTypeInternship,
		finder.JobTypeContract, finder.JobTypeTemporary:
		return true
	default:
		return false
	}
}

func validDescriptionFormat(value string) bool {
	switch finder.DescriptionFormat(value) {
	case finder.DescriptionMarkdown, finder.DescriptionHTML, finder.DescriptionPlain:
		return true
	default:
		return false
	}
}
