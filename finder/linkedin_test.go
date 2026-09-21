package finder

import (
	"errors"
	"math"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeSearchOptions(t *testing.T) {
	options, err := normalizeSearchOptions(SearchOptions{Query: "  golang developer "})
	if err != nil {
		t.Fatal(err)
	}
	if options.Query != "golang developer" {
		t.Errorf("Query = %q, want %q", options.Query, "golang developer")
	}
	if options.Distance != 50 {
		t.Errorf("Distance = %d, want 50", options.Distance)
	}
	if options.ResultsWanted != 15 {
		t.Errorf("ResultsWanted = %d, want 15", options.ResultsWanted)
	}
	if options.DescriptionFormat != DescriptionMarkdown {
		t.Errorf("DescriptionFormat = %q, want %q", options.DescriptionFormat, DescriptionMarkdown)
	}

	options, err = normalizeSearchOptions(SearchOptions{
		Distance:          10,
		ResultsWanted:     40,
		JobType:           JobTypeContract,
		DescriptionFormat: DescriptionPlain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.Distance != 10 || options.ResultsWanted != 40 || options.JobType != JobTypeContract || options.DescriptionFormat != DescriptionPlain {
		t.Errorf("explicit values were overridden: %+v", options)
	}
}

func TestNormalizeSearchOptionsErrors(t *testing.T) {
	tests := []struct {
		name    string
		options SearchOptions
		want    string
	}{
		{name: "distance", options: SearchOptions{Distance: -1}, want: "distance must not be negative"},
		{name: "results wanted", options: SearchOptions{ResultsWanted: -1}, want: "results wanted must not be negative"},
		{name: "offset", options: SearchOptions{Offset: -10}, want: "offset must not be negative"},
		{name: "hours old", options: SearchOptions{HoursOld: -24}, want: "hours old must not be negative"},
		{name: "hours old overflow", options: SearchOptions{HoursOld: math.MaxInt64/3600 + 1}, want: "hours old is too large"},
		{name: "description format", options: SearchOptions{DescriptionFormat: "rtf"}, want: `unsupported description format "rtf"`},
		{name: "job type", options: SearchOptions{JobType: "freelance"}, want: `unsupported job type "freelance"`},
		{name: "job type without search filter", options: SearchOptions{JobType: JobTypeVolunteer}, want: `unsupported job type "volunteer"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeSearchOptions(test.options)
			if err == nil || err.Error() != test.want {
				t.Errorf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRequestedLocations(t *testing.T) {
	tests := []struct {
		name      string
		locations []string
		want      []string
	}{
		{name: "none", locations: nil, want: []string{""}},
		{name: "single blank", locations: []string{"  "}, want: []string{""}},
		{name: "all blank", locations: []string{"", " "}, want: []string{""}},
		{name: "trimmed", locations: []string{" Jakarta "}, want: []string{"Jakarta"}},
		{
			name:      "duplicates and blanks",
			locations: []string{"Jakarta", "", "jakarta", "Singapore", " JAKARTA "},
			want:      []string{"Jakarta", "Singapore"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := requestedLocations(SearchOptions{Locations: test.locations})
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestSearchURL(t *testing.T) {
	client := NewClient(ClientConfig{})
	tests := []struct {
		name     string
		options  SearchOptions
		location string
		start    int
		want     url.Values
	}{
		{
			name:    "minimal",
			options: SearchOptions{Distance: 50},
			want: url.Values{
				"distance": {"50"},
				"pageNum":  {"0"},
				"start":    {"0"},
			},
		},
		{
			name: "all filters",
			options: SearchOptions{
				Query:         "site reliability",
				Distance:      25,
				RemoteOnly:    true,
				JobType:       JobTypeFullTime,
				EasyApplyOnly: true,
				HoursOld:      24,
				CompanyIDs:    []int64{1441, 1035},
			},
			location: "Jakarta, Indonesia",
			start:    30,
			want: url.Values{
				"keywords": {"site reliability"},
				"location": {"Jakarta, Indonesia"},
				"distance": {"25"},
				"pageNum":  {"0"},
				"start":    {"30"},
				"f_WT":     {"2"},
				"f_JT":     {"F"},
				"f_AL":     {"true"},
				"f_TPR":    {"r86400"},
				"f_C":      {"1441,1035"},
			},
		},
		{
			name:    "internship",
			options: SearchOptions{Distance: 50, JobType: JobTypeInternship},
			start:   10,
			want: url.Values{
				"distance": {"50"},
				"pageNum":  {"0"},
				"start":    {"10"},
				"f_JT":     {"I"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rawURL := client.searchURL(test.options, test.location, test.start)
			parsed, err := url.Parse(rawURL)
			if err != nil {
				t.Fatal(err)
			}
			if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search" {
				t.Errorf("endpoint = %q", got)
			}
			if got := parsed.Query(); !reflect.DeepEqual(got, test.want) {
				t.Errorf("query = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSearchURLTrimsBaseURL(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.baseURL = "http://127.0.0.1:8080/"
	rawURL := client.searchURL(SearchOptions{Distance: 50}, "", 0)
	if !strings.HasPrefix(rawURL, "http://127.0.0.1:8080/jobs-guest/") {
		t.Errorf("url = %q", rawURL)
	}
}

func TestJobTypeCode(t *testing.T) {
	tests := []struct {
		jobType JobType
		want    string
	}{
		{JobTypeFullTime, "F"},
		{JobTypeContract, "C"},
		{JobTypeVolunteer, ""},
		{"", ""},
	}
	for _, test := range tests {
		if got := jobTypeCode(test.jobType); got != test.want {
			t.Errorf("jobTypeCode(%q) = %q, want %q", test.jobType, got, test.want)
		}
	}
}

func TestAppendRoundRobin(t *testing.T) {
	cards := func(ids ...string) []searchCard {
		result := make([]searchCard, len(ids))
		for index, id := range ids {
			result[index] = searchCard{job: Job{ID: id}}
		}
		return result
	}
	ids := func(jobs []Job) []string {
		result := make([]string, len(jobs))
		for index, job := range jobs {
			result[index] = job.ID
		}
		return result
	}

	tests := []struct {
		name    string
		seen    []string
		batches [][]searchCard
		limit   int
		want    []string
	}{
		{
			name:    "interleaves",
			batches: [][]searchCard{cards("a1", "a2", "a3"), cards("b1"), nil, cards("c1", "c2")},
			limit:   10,
			want:    []string{"a1", "b1", "c1", "a2", "c2", "a3"},
		},
		{
			name:    "deduplicates",
			batches: [][]searchCard{cards("x", "a1"), cards("x", "b1"), cards("a1")},
			limit:   10,
			want:    []string{"x", "a1", "b1"},
		},
		{
			name:    "skips previously seen",
			seen:    []string{"a1"},
			batches: [][]searchCard{cards("a1", "a2")},
			limit:   10,
			want:    []string{"a2"},
		},
		{
			name:    "stops at limit",
			batches: [][]searchCard{cards("a1", "a2"), cards("b1", "b2")},
			limit:   3,
			want:    []string{"a1", "b1", "a2"},
		},
		{
			name:  "empty",
			limit: 5,
			want:  []string{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seenIDs := make(map[string]struct{})
			for _, id := range test.seen {
				seenIDs[id] = struct{}{}
			}
			got := ids(appendRoundRobin(nil, seenIDs, test.batches, test.limit))
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestJoinErrors(t *testing.T) {
	if err := joinErrors(nil); err != nil {
		t.Errorf("joinErrors(nil) = %v, want nil", err)
	}
	if err := joinErrors([]error{nil}, nil); err != nil {
		t.Errorf("joinErrors with only nil errors = %v, want nil", err)
	}

	first := errors.New("first")
	second := errors.New("second")
	err := joinErrors([]error{first, nil}, second)
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Errorf("joined error %v does not wrap both errors", err)
	}
}
