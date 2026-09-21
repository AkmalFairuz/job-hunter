package finder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTransport struct {
	mutex    sync.Mutex
	requests []*http.Request
	handle   func(*http.Request) *http.Response
}

func (transport *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.mutex.Lock()
	transport.requests = append(transport.requests, req)
	transport.mutex.Unlock()
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	resp := transport.handle(req)
	resp.Request = req
	return resp, nil
}

func (transport *fakeTransport) requestURLs() []string {
	transport.mutex.Lock()
	defer transport.mutex.Unlock()
	urls := make([]string, len(transport.requests))
	for index, req := range transport.requests {
		urls[index] = req.URL.String()
	}
	return urls
}

func newResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func searchPage(ids ...string) string {
	var builder strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&builder, `<li><div class="base-search-card">
	<a class="base-card__full-link" href="https://www.linkedin.com/jobs/view/engineer-%[1]s?refId=x"><span class="sr-only">Engineer %[1]s</span></a>
	<h4 class="base-search-card__subtitle"><a href="https://www.linkedin.com/company/acme?trk=x">Acme</a></h4>
</div></li>
`, id)
	}
	return builder.String()
}

func numberedIDs(first, count int) []string {
	ids := make([]string, count)
	for index := range ids {
		ids[index] = fmt.Sprint(first + index)
	}
	return ids
}

func jobIDs(jobs []Job) []string {
	ids := make([]string, len(jobs))
	for index, job := range jobs {
		ids[index] = job.ID
	}
	return ids
}

type sleepRecorder struct {
	mutex  sync.Mutex
	delays []time.Duration
}

func (recorder *sleepRecorder) sleep(ctx context.Context, delay time.Duration) error {
	recorder.mutex.Lock()
	recorder.delays = append(recorder.delays, delay)
	recorder.mutex.Unlock()
	return ctx.Err()
}

func newTestClient(handle func(*http.Request) *http.Response) (*Client, *fakeTransport, *sleepRecorder) {
	transport := &fakeTransport{handle: handle}
	client := NewClient(ClientConfig{HTTPClient: &http.Client{Transport: transport}})
	recorder := &sleepRecorder{}
	client.sleep = recorder.sleep
	return client, transport, recorder
}

func TestSearchRoundRobinAcrossLocations(t *testing.T) {
	pages := map[string]string{
		"Jakarta":   searchPage("101", "102", "103"),
		"Singapore": searchPage("102", "201"),
	}
	client, transport, _ := newTestClient(func(req *http.Request) *http.Response {
		query := req.URL.Query()
		if query.Get("start") != "0" {
			return newResponse(http.StatusOK, "")
		}
		return newResponse(http.StatusOK, pages[query.Get("location")])
	})

	jobs, err := client.Search(context.Background(), SearchOptions{
		Query:         "engineer",
		Locations:     []string{"Jakarta", "Singapore", "jakarta"},
		ResultsWanted: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"li-101", "li-102", "li-201", "li-103"}
	if got := jobIDs(jobs); !reflect.DeepEqual(got, want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
	if got := len(transport.requestURLs()); got != 4 {
		t.Errorf("made %d requests, want 4", got)
	}
}

func TestSearchStopsAtResultsWanted(t *testing.T) {
	client, transport, _ := newTestClient(func(req *http.Request) *http.Response {
		switch req.URL.Query().Get("location") {
		case "Jakarta":
			return newResponse(http.StatusOK, searchPage("101", "102"))
		default:
			return newResponse(http.StatusOK, searchPage("201", "202"))
		}
	})

	jobs, err := client.Search(context.Background(), SearchOptions{
		Locations:     []string{"Jakarta", "Singapore"},
		ResultsWanted: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"li-101", "li-201", "li-102"}
	if got := jobIDs(jobs); !reflect.DeepEqual(got, want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
	if got := len(transport.requestURLs()); got != 2 {
		t.Errorf("made %d requests, want 2", got)
	}
}

func TestSearchPaginates(t *testing.T) {
	client, transport, recorder := newTestClient(func(req *http.Request) *http.Response {
		switch req.URL.Query().Get("start") {
		case "20":
			return newResponse(http.StatusOK, searchPage(numberedIDs(1, 10)...))
		case "30":
			return newResponse(http.StatusOK, searchPage(numberedIDs(11, 10)...))
		default:
			return newResponse(http.StatusOK, "")
		}
	})

	jobs, err := client.Search(context.Background(), SearchOptions{ResultsWanted: 15, Offset: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 15 || jobs[0].ID != "li-1" || jobs[14].ID != "li-15" {
		t.Errorf("jobs = %v", jobIDs(jobs))
	}

	var starts []string
	for _, rawURL := range transport.requestURLs() {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		starts = append(starts, parsed.Query().Get("start"))
	}
	if want := []string{"20", "30"}; !reflect.DeepEqual(starts, want) {
		t.Errorf("requested %v, want %v", starts, want)
	}
	if len(recorder.delays) != 1 {
		t.Fatalf("slept %d times, want 1", len(recorder.delays))
	}
	if delay := recorder.delays[0]; delay < client.minPageDelay || delay > client.maxPageDelay {
		t.Errorf("page delay = %v, want between %v and %v", delay, client.minPageDelay, client.maxPageDelay)
	}
}

func TestSearchReturnsPartialResults(t *testing.T) {
	client, _, _ := newTestClient(func(req *http.Request) *http.Response {
		query := req.URL.Query()
		if query.Get("location") == "Singapore" {
			return newResponse(http.StatusNotFound, "")
		}
		if query.Get("start") != "0" {
			return newResponse(http.StatusOK, "")
		}
		return newResponse(http.StatusOK, searchPage("101", "102"))
	})

	jobs, err := client.Search(context.Background(), SearchOptions{
		Locations:     []string{"Jakarta", "Singapore"},
		ResultsWanted: 10,
	})
	if want := []string{"li-101", "li-102"}; !reflect.DeepEqual(jobIDs(jobs), want) {
		t.Errorf("jobs = %v, want %v", jobIDs(jobs), want)
	}
	var searchErr *SearchError
	if !errors.As(err, &searchErr) {
		t.Fatalf("err = %v, want *SearchError", err)
	}
	if searchErr.Operation != "search request" || searchErr.StatusCode != http.StatusNotFound {
		t.Errorf("error = %+v", searchErr)
	}
	if !strings.Contains(searchErr.URL, "location=Singapore") {
		t.Errorf("error URL = %q", searchErr.URL)
	}
}

func TestSearchRetries(t *testing.T) {
	var transport *fakeTransport
	client, transport, recorder := newTestClient(func(req *http.Request) *http.Response {
		if len(transport.requestURLs()) == 1 {
			resp := newResponse(http.StatusServiceUnavailable, "")
			resp.Header.Set("Retry-After", "12")
			return resp
		}
		return newResponse(http.StatusOK, searchPage("101"))
	})

	jobs, err := client.Search(context.Background(), SearchOptions{ResultsWanted: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Errorf("got %d jobs, want 1", len(jobs))
	}
	if got := len(transport.requestURLs()); got != 2 {
		t.Errorf("made %d requests, want 2", got)
	}
	if want := []time.Duration{12 * time.Second}; !reflect.DeepEqual(recorder.delays, want) {
		t.Errorf("delays = %v, want %v", recorder.delays, want)
	}
}

func TestSearchRateLimited(t *testing.T) {
	client, transport, recorder := newTestClient(func(req *http.Request) *http.Response {
		return newResponse(http.StatusTooManyRequests, "")
	})

	jobs, err := client.Search(context.Background(), SearchOptions{})
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want none", len(jobs))
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
	var searchErr *SearchError
	if !errors.As(err, &searchErr) || searchErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("err = %v, want *SearchError with status 429", err)
	}
	if got := len(transport.requestURLs()); got != 4 {
		t.Errorf("made %d requests, want 4", got)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	if !reflect.DeepEqual(recorder.delays, want) {
		t.Errorf("delays = %v, want %v", recorder.delays, want)
	}
}

func TestSearchRejectsInvalidOptions(t *testing.T) {
	client, transport, _ := newTestClient(func(req *http.Request) *http.Response {
		return newResponse(http.StatusOK, "")
	})
	jobs, err := client.Search(context.Background(), SearchOptions{JobType: "freelance"})
	if err == nil || jobs != nil {
		t.Errorf("Search = %v, %v; want nil jobs and an error", jobs, err)
	}
	if got := len(transport.requestURLs()); got != 0 {
		t.Errorf("made %d requests, want 0", got)
	}
}

func TestSearchCanceled(t *testing.T) {
	client, transport, _ := newTestClient(func(req *http.Request) *http.Response {
		return newResponse(http.StatusOK, searchPage("101"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Search(ctx, SearchOptions{Locations: []string{"Jakarta", "Singapore"}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := len(transport.requestURLs()); got != 0 {
		t.Errorf("made %d requests, want 0", got)
	}
}

func TestSearchFetchesDescriptions(t *testing.T) {
	detail, err := os.ReadFile(filepath.Join("testdata", "job_detail.html"))
	if err != nil {
		t.Fatal(err)
	}
	client, _, _ := newTestClient(func(req *http.Request) *http.Response {
		switch {
		case req.URL.Path == "/jobs/view/3912345601":
			return newResponse(http.StatusOK, string(detail))
		case strings.HasPrefix(req.URL.Path, "/jobs/view/"):
			resp := newResponse(http.StatusFound, "")
			resp.Header.Set("Location", "https://www.linkedin.com/signup/cold-join")
			return resp
		case req.URL.Path == "/signup/cold-join":
			return newResponse(http.StatusOK, "<html>Join LinkedIn</html>")
		case req.URL.Query().Get("start") == "0":
			return newResponse(http.StatusOK, searchPage("3912345601", "3912345602"))
		default:
			return newResponse(http.StatusOK, "")
		}
	})

	jobs, err := client.Search(context.Background(), SearchOptions{
		ResultsWanted:     2,
		FetchDescription:  true,
		DescriptionFormat: DescriptionPlain,
	})
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	enriched := jobs[0]
	if !strings.HasPrefix(enriched.Description, "About the role Build services in Go.") {
		t.Errorf("Description = %q", enriched.Description)
	}
	if want := []string{"jobs@acme.example"}; !reflect.DeepEqual(enriched.Emails, want) {
		t.Errorf("Emails = %v, want %v", enriched.Emails, want)
	}
	if enriched.JobLevel != "mid-senior level" {
		t.Errorf("JobLevel = %q, want %q", enriched.JobLevel, "mid-senior level")
	}
	if got := enriched.Location.String(); got != "Jakarta, Jakarta, Indonesia" {
		t.Errorf("Location = %q, want %q", got, "Jakarta, Jakarta, Indonesia")
	}
	if jobs[1].Description != "" {
		t.Errorf("redirected job Description = %q, want empty", jobs[1].Description)
	}
	var searchErr *SearchError
	if !errors.As(err, &searchErr) {
		t.Fatalf("err = %v, want *SearchError", err)
	}
	if searchErr.Operation != "job detail request" || !strings.HasSuffix(searchErr.URL, "/jobs/view/3912345602") {
		t.Errorf("error = %+v", searchErr)
	}
}

func TestClientSetsHeaders(t *testing.T) {
	transport := &fakeTransport{handle: func(req *http.Request) *http.Response {
		return newResponse(http.StatusOK, "")
	}}
	tests := []struct {
		name      string
		userAgent string
		want      string
	}{
		{name: "default", userAgent: "  ", want: defaultUserAgent},
		{name: "custom", userAgent: "job-hunter-test/1.0", want: "job-hunter-test/1.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(ClientConfig{
				HTTPClient: &http.Client{Transport: transport},
				UserAgent:  test.userAgent,
			})
			resp, err := client.get(context.Background(), linkedinBaseURL+"/jobs", "test", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := resp.Request.Header.Get("User-Agent"); got != test.want {
				t.Errorf("User-Agent = %q, want %q", got, test.want)
			}
			if got := resp.Request.Header.Get("Accept-Language"); got != "en-US,en;q=0.9" {
				t.Errorf("Accept-Language = %q", got)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "empty", value: "", want: 0},
		{name: "seconds", value: "30", want: 30 * time.Second},
		{name: "negative", value: "-5", want: 0},
		{name: "future date", value: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second},
		{name: "past date", value: now.Add(-time.Minute).Format(http.TimeFormat), want: 0},
		{name: "invalid", value: "soon", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseRetryAfter(test.value, now); got != test.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestPageDelay(t *testing.T) {
	client := NewClient(ClientConfig{})
	for range 100 {
		if delay := client.pageDelay(); delay < client.minPageDelay || delay > client.maxPageDelay {
			t.Fatalf("pageDelay() = %v, want between %v and %v", delay, client.minPageDelay, client.maxPageDelay)
		}
	}

	client.maxPageDelay = client.minPageDelay
	if delay := client.pageDelay(); delay != client.minPageDelay {
		t.Errorf("pageDelay() = %v, want %v", delay, client.minPageDelay)
	}
}

func TestSleepContext(t *testing.T) {
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Errorf("zero delay: %v", err)
	}
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Errorf("short delay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v, want context.Canceled", err)
	}
}

func TestSearchError(t *testing.T) {
	cause := errors.New("connection reset")
	tests := []struct {
		name string
		err  *SearchError
		want string
	}{
		{name: "nil", err: nil, want: "<nil>"},
		{name: "operation only", err: &SearchError{Operation: "search request"}, want: "linkedin search request failed"},
		{name: "status", err: &SearchError{Operation: "search request", StatusCode: 404}, want: "linkedin search request failed with status 404"},
		{
			name: "status and cause",
			err:  &SearchError{Operation: "job detail request", StatusCode: 429, Err: ErrRateLimited},
			want: "linkedin job detail request failed with status 429: linkedin rate limited",
		},
		{name: "cause", err: &SearchError{Operation: "search request", Err: cause}, want: "linkedin search request failed: connection reset"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Errorf("Error() = %q, want %q", got, test.want)
			}
		})
	}

	var nilErr *SearchError
	if nilErr.Unwrap() != nil {
		t.Error("nil Unwrap should return nil")
	}
	if err := error(&SearchError{Operation: "search request", Err: cause}); !errors.Is(err, cause) {
		t.Errorf("errors.Is(%v, cause) = false", err)
	}
}
