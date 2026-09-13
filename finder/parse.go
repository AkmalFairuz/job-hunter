package finder

import (
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
)

var (
	emailPattern    = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	applyURLPattern = regexp.MustCompile(`\?url=([^"<]+)`)
	salarySeparator = regexp.MustCompile(`\s*[-—–]\s*`)
)

type searchCard struct {
	job Job
}

type jobDetails struct {
	description     string
	jobTypes        []JobType
	jobLevel        string
	companyIndustry string
	jobURLDirect    string
	companyLogoURL  string
	jobFunction     string
	location        Location
}

func parseSearchPage(reader io.Reader, baseURL string) ([]searchCard, int, error) {
	document, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return nil, 0, err
	}
	cards := make([]searchCard, 0)
	selections := document.Find("div.base-search-card")
	selections.Each(func(_ int, selection *goquery.Selection) {
		card, ok := parseSearchCard(selection, baseURL)
		if ok {
			cards = append(cards, card)
		}
	})
	return cards, selections.Length(), nil
}

func parseSearchCard(card *goquery.Selection, baseURL string) (searchCard, bool) {
	href, exists := card.Find("a.base-card__full-link").First().Attr("href")
	if !exists || strings.TrimSpace(href) == "" {
		return searchCard{}, false
	}
	jobID := jobIDFromURL(href)
	if jobID == "" {
		return searchCard{}, false
	}

	title := cleanText(card.Find("span.sr-only").First().Text())
	if title == "" {
		title = "N/A"
	}
	companyLink := card.Find("h4.base-search-card__subtitle a").First()
	company := cleanText(companyLink.Text())
	if company == "" {
		company = "N/A"
	}
	companyURL, _ := companyLink.Attr("href")
	companyURL = stripURLQuery(companyURL)

	metadata := card.Find("div.base-search-card__metadata").First()
	location := parseLocation(metadata.Find("span.job-search-card__location").First().Text())
	datePosted := parseDatePosted(metadata)
	compensation := parseCompensation(card.Find("span.job-search-card__salary-info").First().Text())
	companyLogoURL := findImageURL(card.Find("img.artdeco-entity-image").First())
	job := Job{
		ID:             "li-" + jobID,
		Title:          title,
		CompanyName:    company,
		CompanyURL:     companyURL,
		Location:       location,
		IsRemote:       isRemote(title, "", location),
		DatePosted:     datePosted,
		JobURL:         strings.TrimRight(baseURL, "/") + "/jobs/view/" + jobID,
		Compensation:   compensation,
		CompanyLogoURL: companyLogoURL,
	}
	return searchCard{job: job}, true
}

func parseJobDetails(reader io.Reader, format DescriptionFormat) (jobDetails, error) {
	document, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return jobDetails{}, err
	}

	details := jobDetails{
		jobTypes:        parseEmploymentTypes(document),
		jobLevel:        findCriterion(document, "Seniority level"),
		companyIndustry: findCriterion(document, "Industries"),
		jobFunction:     findCriterion(document, "Job function"),
	}
	details.companyLogoURL = findImageURL(document.Find("img.artdeco-entity-image").First())
	details.jobURLDirect = parseDirectURL(document)
	details.location = parseLocation(document.Find("span.topcard__flavor--bullet").First().Text())
	if details.location.String() == "" {
		details.location = parseLocation(document.Find("span.sub-nav-cta__meta-text").First().Text())
	}

	description := document.Find("div").FilterFunction(func(_ int, selection *goquery.Selection) bool {
		class, exists := selection.Attr("class")
		return exists && strings.Contains(class, "show-more-less-html__markup")
	}).First()
	if description.Length() == 0 {
		return details, nil
	}
	for _, node := range description.Nodes {
		node.Attr = nil
	}
	descriptionHTML, err := goquery.OuterHtml(description)
	if err != nil {
		return details, fmt.Errorf("render description: %w", err)
	}

	switch format {
	case DescriptionHTML:
		details.description = strings.TrimSpace(descriptionHTML)
	case DescriptionPlain:
		details.description = cleanText(description.Text())
	case DescriptionMarkdown:
		markdown, conversionErr := htmltomarkdown.ConvertString(descriptionHTML)
		if conversionErr != nil {
			return details, fmt.Errorf("convert description to markdown: %w", conversionErr)
		}
		details.description = strings.TrimSpace(markdown)
	default:
		return details, fmt.Errorf("unsupported description format %q", format)
	}
	return details, nil
}

func findImageURL(image *goquery.Selection) string {
	for _, attribute := range []string{"data-delayed-url", "src"} {
		if value, exists := image.Attr(attribute); exists && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func findCriterion(document *goquery.Document, label string) string {
	value := ""
	document.Find("h3.description__job-criteria-subheader").EachWithBreak(func(_ int, heading *goquery.Selection) bool {
		if !strings.Contains(cleanText(heading.Text()), label) {
			return true
		}
		value = cleanText(heading.NextFiltered("span.description__job-criteria-text").First().Text())
		return false
	})
	return value
}

func parseEmploymentTypes(document *goquery.Document) []JobType {
	employmentType := normalizeJobType(findCriterion(document, "Employment type"))
	if employmentType == "" {
		return nil
	}
	return []JobType{employmentType}
}

func normalizeJobType(value string) JobType {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "")
	aliases := map[JobType][]string{
		JobTypeFullTime: {
			"fulltime", "períodointegral", "estágio/trainee", "cunormăîntreagă",
			"tiempocompleto", "vollzeit", "voltijds", "tempointegral", "全职",
			"plnýúvazek", "fuldtid", "دوامكامل", "kokopäivätyö", "tempsplein",
			"πλήρηςαπασχόληση", "teljesmunkaidő", "tempopieno", "heltid",
			"jornadacompleta", "pełnyetat", "정규직", "100%", "全職", "งานประจำ",
			"tamzamanlı", "повназайнятість", "toànthờigian",
		},
		JobTypePartTime:   {"parttime", "teilzeit", "částečnýúvazek", "deltid"},
		JobTypeContract:   {"contract", "contractor"},
		JobTypeTemporary:  {"temporary"},
		JobTypeInternship: {"internship", "prácticas", "ojt(onthejobtraining)", "praktikum", "praktik"},
		JobTypePerDiem:    {"perdiem"},
		JobTypeNights:     {"nights"},
		JobTypeOther:      {"other"},
		JobTypeSummer:     {"summer"},
		JobTypeVolunteer:  {"volunteer"},
	}
	for jobType, values := range aliases {
		for _, alias := range values {
			if normalized == alias {
				return jobType
			}
		}
	}
	return ""
}

func parseDirectURL(document *goquery.Document) string {
	content, err := document.Find("code#applyUrl").First().Html()
	if err != nil || content == "" {
		return ""
	}
	content = html.UnescapeString(content)
	match := applyURLPattern.FindStringSubmatch(content)
	if len(match) != 2 {
		return ""
	}
	decoded, err := url.PathUnescape(match[1])
	if err != nil {
		return ""
	}
	return decoded
}

func parseLocation(value string) Location {
	location := Location{Country: "worldwide"}
	cleaned := cleanText(value)
	if cleaned == "" {
		return location
	}
	parts := strings.Split(cleaned, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	switch len(parts) {
	case 1:
		location.City = parts[0]
	case 2:
		location.City = parts[0]
		location.State = parts[1]
	case 3:
		location.City = parts[0]
		location.State = parts[1]
		location.Country = parts[2]
	}
	return location
}

func parseDatePosted(metadata *goquery.Selection) *time.Time {
	dateElement := metadata.Find("time.job-search-card__listdate").First()
	if dateElement.Length() == 0 {
		dateElement = metadata.Find("time.job-search-card__listdate--new").First()
	}
	value, exists := dateElement.Attr("datetime")
	if !exists {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseCompensation(value string) *Compensation {
	value = cleanText(value)
	if value == "" {
		return nil
	}
	parts := salarySeparator.Split(value, 2)
	if len(parts) != 2 {
		return nil
	}
	minimum, ok := parseMoney(parts[0])
	if !ok {
		return nil
	}
	maximum, ok := parseMoney(parts[1])
	if !ok {
		return nil
	}
	return &Compensation{
		MinAmount: minimum,
		MaxAmount: maximum,
		Currency:  detectCurrency(value),
	}
}

func parseMoney(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	multiplier := float64(1)
	lower := strings.ToLower(value)
	if strings.Contains(lower, "k") {
		multiplier = 1000
	} else if strings.Contains(lower, "m") {
		multiplier = 1_000_000
	}

	var builder strings.Builder
	for _, char := range value {
		if unicode.IsDigit(char) || char == '.' || char == ',' || char == '-' {
			builder.WriteRune(char)
		}
	}
	numeric := builder.String()
	if numeric == "" {
		return 0, false
	}
	lastComma := strings.LastIndex(numeric, ",")
	lastDot := strings.LastIndex(numeric, ".")
	switch {
	case lastComma >= 0 && lastDot >= 0:
		if lastComma > lastDot {
			numeric = strings.ReplaceAll(numeric, ".", "")
			numeric = strings.ReplaceAll(numeric, ",", ".")
		} else {
			numeric = strings.ReplaceAll(numeric, ",", "")
		}
	case lastComma >= 0:
		if len(numeric)-lastComma-1 == 3 {
			numeric = strings.ReplaceAll(numeric, ",", "")
		} else {
			numeric = strings.ReplaceAll(numeric, ",", ".")
		}
	case lastDot >= 0:
		if len(numeric)-lastDot-1 == 3 {
			numeric = strings.ReplaceAll(numeric, ".", "")
		}
	}
	amount, err := strconv.ParseFloat(numeric, 64)
	return amount * multiplier, err == nil
}

func detectCurrency(value string) string {
	if strings.Contains(value, "$") {
		prefix := strings.TrimSpace(value[:strings.Index(value, "$")])
		if prefix != "" {
			return prefix + "$"
		}
		return "USD"
	}
	for _, char := range value {
		if unicode.IsSymbol(char) {
			return string(char)
		}
		if unicode.IsDigit(char) {
			break
		}
	}
	return ""
}

func jobIDFromURL(value string) string {
	if parsed, err := url.Parse(strings.TrimSpace(value)); err == nil {
		value = parsed.Path
	} else if before, _, found := strings.Cut(value, "?"); found {
		value = before
	}
	value = strings.TrimRight(value, "/")
	segment := value[strings.LastIndex(value, "/")+1:]
	if index := strings.LastIndex(segment, "-"); index >= 0 {
		return segment[index+1:]
	}
	return segment
}

func stripURLQuery(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	return parsed.String()
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func isRemote(title, description string, location Location) bool {
	combined := strings.ToLower(title + " " + description + " " + location.String())
	for _, keyword := range []string{"remote", "work from home", "wfh"} {
		if strings.Contains(combined, keyword) {
			return true
		}
	}
	return false
}

func extractEmails(value string) []string {
	if value == "" {
		return nil
	}
	emails := emailPattern.FindAllString(value, -1)
	if len(emails) == 0 {
		return nil
	}
	return emails
}
