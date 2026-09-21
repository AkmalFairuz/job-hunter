package finder

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func parseDocument(t *testing.T, markup string) *goquery.Document {
	t.Helper()
	document, err := goquery.NewDocumentFromReader(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestParseSearchPage(t *testing.T) {
	cards, pageSize, err := parseSearchPage(openFixture(t, "search_page.html"), "https://www.linkedin.com/")
	if err != nil {
		t.Fatal(err)
	}
	if pageSize != 4 {
		t.Errorf("pageSize = %d, want 4", pageSize)
	}
	if len(cards) != 3 {
		t.Fatalf("got %d cards, want 3", len(cards))
	}

	posted := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	want := Job{
		ID:          "li-3912345601",
		Title:       "Backend Engineer",
		CompanyName: "Acme",
		CompanyURL:  "https://id.linkedin.com/company/acme",
		Location:    Location{City: "Jakarta", State: "Jakarta", Country: "Indonesia"},
		DatePosted:  &posted,
		JobURL:      "https://www.linkedin.com/jobs/view/3912345601",
		Compensation: &Compensation{
			MinAmount: 120000,
			MaxAmount: 150000,
			Currency:  "USD",
		},
		CompanyLogoURL: "https://media.licdn.com/dms/image/acme-logo.png",
	}
	if got := cards[0].job; !reflect.DeepEqual(got, want) {
		t.Errorf("first card:\n got %+v\nwant %+v", got, want)
	}

	remote := cards[1].job
	if remote.ID != "li-3912345602" || remote.Title != "Go Developer (Remote)" {
		t.Errorf("second card = %q %q", remote.ID, remote.Title)
	}
	if !remote.IsRemote {
		t.Error("second card should be remote")
	}
	if remote.Location != (Location{City: "Bandung", State: "West Java", Country: "worldwide"}) {
		t.Errorf("second card location = %+v", remote.Location)
	}
	if remote.DatePosted == nil || !remote.DatePosted.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("second card date = %v", remote.DatePosted)
	}
	if remote.Compensation != nil {
		t.Errorf("second card compensation = %+v, want nil", remote.Compensation)
	}
	if remote.CompanyLogoURL != "https://media.licdn.com/dms/image/globex-logo.png" {
		t.Errorf("second card logo = %q", remote.CompanyLogoURL)
	}

	sparse := cards[2].job
	if sparse.ID != "li-3912345604" || sparse.Title != "N/A" || sparse.CompanyName != "N/A" {
		t.Errorf("third card = %q %q %q", sparse.ID, sparse.Title, sparse.CompanyName)
	}
	if sparse.DatePosted != nil {
		t.Errorf("third card date = %v, want nil", sparse.DatePosted)
	}
}

func TestParseSearchPageEmpty(t *testing.T) {
	cards, pageSize, err := parseSearchPage(strings.NewReader(""), linkedinBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 || pageSize != 0 {
		t.Errorf("got %d cards and page size %d, want none", len(cards), pageSize)
	}
}

func TestParseSearchCardSkipsInvalidLinks(t *testing.T) {
	tests := map[string]string{
		"missing link": `<div class="base-search-card"></div>`,
		"blank href":   `<div class="base-search-card"><a class="base-card__full-link" href="  "></a></div>`,
		"no job id":    `<div class="base-search-card"><a class="base-card__full-link" href="/"></a></div>`,
	}
	for name, markup := range tests {
		t.Run(name, func(t *testing.T) {
			card := parseDocument(t, markup).Find("div.base-search-card")
			if _, ok := parseSearchCard(card, linkedinBaseURL); ok {
				t.Error("card should be skipped")
			}
		})
	}
}

func TestParseJobDetails(t *testing.T) {
	details, err := parseJobDetails(openFixture(t, "job_detail.html"), DescriptionMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	want := jobDetails{
		description:     details.description,
		jobTypes:        []JobType{JobTypeFullTime},
		jobLevel:        "Mid-Senior level",
		companyIndustry: "Software Development",
		jobFunction:     "Engineering and Information Technology",
		jobURLDirect:    "https://careers.acme.example/jobs/42",
		companyLogoURL:  "https://media.licdn.com/dms/image/acme-logo-large.png",
		location:        Location{City: "Jakarta", State: "Jakarta", Country: "Indonesia"},
	}
	if !reflect.DeepEqual(details, want) {
		t.Errorf("details:\n got %+v\nwant %+v", details, want)
	}
}

func TestParseJobDetailsFormats(t *testing.T) {
	tests := []struct {
		format      DescriptionFormat
		contains    []string
		notContains []string
	}{
		{
			format:      DescriptionMarkdown,
			contains:    []string{"**About the role**", "- Design APIs", "- Review code", "jobs@acme.example"},
			notContains: []string{"<p>", "show-more-less-html"},
		},
		{
			format:      DescriptionHTML,
			contains:    []string{"<div>", "<strong>About the role</strong>", "<li>Design APIs</li>"},
			notContains: []string{"class=", "show-more-less-html"},
		},
		{
			format:      DescriptionPlain,
			contains:    []string{"About the role Build services in Go. Design APIs Review code"},
			notContains: []string{"<", "**", "\n"},
		},
	}
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			details, err := parseJobDetails(openFixture(t, "job_detail.html"), test.format)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range test.contains {
				if !strings.Contains(details.description, fragment) {
					t.Errorf("description missing %q:\n%s", fragment, details.description)
				}
			}
			for _, fragment := range test.notContains {
				if strings.Contains(details.description, fragment) {
					t.Errorf("description contains %q:\n%s", fragment, details.description)
				}
			}
		})
	}
}

func TestParseJobDetailsWithoutDescription(t *testing.T) {
	markup := `<span class="sub-nav-cta__meta-text">Singapore</span>`
	details, err := parseJobDetails(strings.NewReader(markup), DescriptionMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if details.description != "" {
		t.Errorf("description = %q, want empty", details.description)
	}
	if details.jobTypes != nil {
		t.Errorf("jobTypes = %v, want nil", details.jobTypes)
	}
	if details.location.City != "Singapore" {
		t.Errorf("location = %+v, want fallback city Singapore", details.location)
	}
}

func TestParseJobDetailsUnsupportedFormat(t *testing.T) {
	_, err := parseJobDetails(openFixture(t, "job_detail.html"), "pdf")
	if err == nil || !strings.Contains(err.Error(), `unsupported description format "pdf"`) {
		t.Errorf("err = %v", err)
	}
}

func TestFindCriterion(t *testing.T) {
	document := parseDocument(t, `
		<h3 class="description__job-criteria-subheader">Seniority level</h3>
		<span class="description__job-criteria-text">Entry level</span>
		<h3 class="description__job-criteria-subheader">Industries</h3>
		<p>unexpected sibling</p>
		<span class="description__job-criteria-text">Retail</span>`)

	tests := []struct {
		name  string
		label string
		want  string
	}{
		{name: "found", label: "Seniority level", want: "Entry level"},
		{name: "partial label", label: "Seniority", want: "Entry level"},
		{name: "sibling mismatch", label: "Industries", want: ""},
		{name: "missing", label: "Job function", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := findCriterion(document, test.label); got != test.want {
				t.Errorf("findCriterion(%q) = %q, want %q", test.label, got, test.want)
			}
		})
	}
}

func TestParseEmploymentTypes(t *testing.T) {
	tests := []struct {
		name   string
		markup string
		want   []JobType
	}{
		{
			name: "known",
			markup: `<h3 class="description__job-criteria-subheader">Employment type</h3>
				<span class="description__job-criteria-text">Part-time</span>`,
			want: []JobType{JobTypePartTime},
		},
		{
			name: "unknown",
			markup: `<h3 class="description__job-criteria-subheader">Employment type</h3>
				<span class="description__job-criteria-text">Seasonal</span>`,
			want: nil,
		},
		{name: "missing", markup: `<p>No criteria</p>`, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseEmploymentTypes(parseDocument(t, test.markup)); !reflect.DeepEqual(got, test.want) {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalizeJobType(t *testing.T) {
	tests := []struct {
		value string
		want  JobType
	}{
		{"  FULL-TIME ", JobTypeFullTime},
		{"Vollzeit", JobTypeFullTime},
		{"Contractor", JobTypeContract},
		{"Freelance", ""},
	}
	for _, test := range tests {
		if got := normalizeJobType(test.value); got != test.want {
			t.Errorf("normalizeJobType(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestParseDirectURL(t *testing.T) {
	tests := []struct {
		name   string
		markup string
		want   string
	}{
		{
			name:   "comment",
			markup: `<code id="applyUrl"><!--"https://www.linkedin.com/jobs/view/externalApply/1?url=https%3A%2F%2Fexample%2Ecom%2Fapply%3Fsource%3Dlinkedin"--></code>`,
			want:   "https://example.com/apply?source=linkedin",
		},
		{
			name:   "escaped text",
			markup: `<code id="applyUrl">&#34;https://www.linkedin.com/jobs/view/externalApply/1?url=https%3A%2F%2Fexample.com%2Fjobs&#34;</code>`,
			want:   "https://example.com/jobs",
		},
		{name: "no url parameter", markup: `<code id="applyUrl"><!--"https://www.linkedin.com/jobs/view/1"--></code>`, want: ""},
		{name: "invalid escape", markup: `<code id="applyUrl"><!--"?url=https%3A%2F%2Fexample.com%ZZ"--></code>`, want: ""},
		{name: "empty", markup: `<code id="applyUrl"></code>`, want: ""},
		{name: "missing", markup: `<p>Easy Apply</p>`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseDirectURL(parseDocument(t, test.markup)); got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseLocation(t *testing.T) {
	tests := []struct {
		value string
		want  Location
	}{
		{"", Location{Country: "worldwide"}},
		{"   ", Location{Country: "worldwide"}},
		{"Remote", Location{City: "Remote", Country: "worldwide"}},
		{"Jakarta, Indonesia", Location{City: "Jakarta", State: "Indonesia", Country: "worldwide"}},
		{" San Francisco ,  CA,\n United States ", Location{City: "San Francisco", State: "CA", Country: "United States"}},
	}
	for _, test := range tests {
		if got := parseLocation(test.value); got != test.want {
			t.Errorf("parseLocation(%q) = %+v, want %+v", test.value, got, test.want)
		}
	}
}

func TestLocationString(t *testing.T) {
	tests := []struct {
		location Location
		want     string
	}{
		{Location{}, ""},
		{Location{Country: "worldwide"}, ""},
		{Location{City: "Jakarta", Country: "Worldwide"}, "Jakarta"},
		{Location{City: "Austin", State: "TX", Country: "United States"}, "Austin, TX, United States"},
		{Location{Country: "Germany"}, "Germany"},
	}
	for _, test := range tests {
		if got := test.location.String(); got != test.want {
			t.Errorf("%+v.String() = %q, want %q", test.location, got, test.want)
		}
	}
}

func TestFindImageURL(t *testing.T) {
	tests := []struct {
		name   string
		markup string
		want   string
	}{
		{name: "delayed url", markup: `<img data-delayed-url="https://example.com/delayed.png" src="https://example.com/src.png">`, want: "https://example.com/delayed.png"},
		{name: "blank delayed url", markup: `<img data-delayed-url=" " src=" https://example.com/src.png ">`, want: "https://example.com/src.png"},
		{name: "no attributes", markup: `<img alt="logo">`, want: ""},
		{name: "no image", markup: `<p></p>`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			image := parseDocument(t, test.markup).Find("img").First()
			if got := findImageURL(image); got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseCompensation(t *testing.T) {
	tests := []struct {
		value string
		want  *Compensation
	}{
		{"$120,000.00 - $150,000.00", &Compensation{MinAmount: 120000, MaxAmount: 150000, Currency: "USD"}},
		{"CA$90K – CA$110K", &Compensation{MinAmount: 90000, MaxAmount: 110000, Currency: "CA$"}},
		{"€50.000 - €60.000", &Compensation{MinAmount: 50000, MaxAmount: 60000, Currency: "€"}},
		{"£45,5 - £50,25", &Compensation{MinAmount: 45.5, MaxAmount: 50.25, Currency: "£"}},
		{"$150,000.00", nil},
		{"Competitive - negotiable", nil},
		{"", nil},
	}
	for _, test := range tests {
		got := parseCompensation(test.value)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("parseCompensation(%q) = %+v, want %+v", test.value, got, test.want)
		}
	}
}

func TestJobIDFromURL(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"https://www.linkedin.com/jobs/view/backend-engineer-at-acme-3912345601?refId=abc", "3912345601"},
		{"/jobs/view/3912345601/", "3912345601"},
		{"", ""},
	}
	for _, test := range tests {
		if got := jobIDFromURL(test.value); got != test.want {
			t.Errorf("jobIDFromURL(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestIsRemote(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		location    Location
		want        bool
	}{
		{name: "title", title: "Senior Engineer (Remote)", want: true},
		{name: "description", title: "Engineer", description: "You can Work From Home twice a week", want: true},
		{name: "abbreviation", title: "Engineer - WFH", want: true},
		{name: "location", title: "Engineer", location: Location{City: "Remote", Country: "worldwide"}, want: true},
		{name: "onsite", title: "Engineer", description: "On-site in Jakarta", location: Location{City: "Jakarta"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isRemote(test.title, test.description, test.location); got != test.want {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestExtractEmails(t *testing.T) {
	got := extractEmails("Send your CV to jobs@acme.example or hr.team+it@globex.co.id.")
	want := []string{"jobs@acme.example", "hr.team+it@globex.co.id"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := extractEmails("no contact"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}
