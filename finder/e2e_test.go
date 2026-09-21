//go:build e2e

package finder_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akmalfairuz/job-hunter/finder"
)

func TestSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	jobs, err := finder.NewClient(finder.ClientConfig{}).Search(ctx, finder.SearchOptions{
		Query:         "software engineer",
		Locations:     []string{"United States"},
		ResultsWanted: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 0 {
		t.Fatal("no jobs found")
	}

	for _, job := range jobs {
		if !strings.HasPrefix(job.ID, "li-") {
			t.Fatalf("job id = %q", job.ID)
		}
		if job.Title == "" {
			t.Fatalf("job %s has no title", job.ID)
		}
		if !strings.HasPrefix(job.JobURL, "https://www.linkedin.com/jobs/view/") {
			t.Fatalf("job %s url = %q", job.ID, job.JobURL)
		}
	}
}

func TestSearch_FetchesDescription(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	jobs, err := finder.NewClient(finder.ClientConfig{}).Search(ctx, finder.SearchOptions{
		Query:            "software engineer",
		Locations:        []string{"United States"},
		ResultsWanted:    3,
		FetchDescription: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 0 {
		t.Fatal("no jobs found")
	}

	for _, job := range jobs {
		if job.Description == "" {
			t.Fatalf("job %s has no description", job.ID)
		}
	}
}
