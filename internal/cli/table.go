package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/akmalfairuz/job-hunter/finder"
)

const (
	outputTable = "table"
	outputWide  = "wide"
	outputJSON  = "json"

	missingValue = "<none>"
	unknownAge   = "<unknown>"
)

type tableColumn struct {
	header string
	width  int
	value  func(finder.Job, time.Time) string
}

var defaultColumns = []tableColumn{
	{header: "AGE", width: 9, value: jobAge},
	{header: "TITLE", width: 40, value: func(job finder.Job, _ time.Time) string { return job.Title }},
	{header: "COMPANY", width: 28, value: func(job finder.Job, _ time.Time) string { return job.CompanyName }},
	{header: "LOCATION", width: 28, value: func(job finder.Job, _ time.Time) string { return job.Location.String() }},
	{header: "TYPE", width: 16, value: jobTypes},
	{header: "REMOTE", width: 6, value: func(job finder.Job, _ time.Time) string { return strconv.FormatBool(job.IsRemote) }},
	{header: "URL", width: 48, value: func(job finder.Job, _ time.Time) string { return job.JobURL }},
}

var wideColumns = []tableColumn{
	{header: "ID", width: 16, value: func(job finder.Job, _ time.Time) string { return job.ID }},
	{header: "AGE", width: 9, value: jobAge},
	{header: "TITLE", width: 40, value: func(job finder.Job, _ time.Time) string { return job.Title }},
	{header: "COMPANY", width: 28, value: func(job finder.Job, _ time.Time) string { return job.CompanyName }},
	{header: "LOCATION", width: 28, value: func(job finder.Job, _ time.Time) string { return job.Location.String() }},
	{header: "TYPE", width: 16, value: jobTypes},
	{header: "LEVEL", width: 18, value: func(job finder.Job, _ time.Time) string { return job.JobLevel }},
	{header: "REMOTE", width: 6, value: func(job finder.Job, _ time.Time) string { return strconv.FormatBool(job.IsRemote) }},
	{header: "SALARY", width: 22, value: jobSalary},
	{header: "URL", width: 48, value: func(job finder.Job, _ time.Time) string { return job.JobURL }},
}

func writeSearchOutput(writer io.Writer, result searchOutput, format string, pretty bool) error {
	switch format {
	case outputTable:
		return writeTable(writer, result.Jobs, result.FetchedAt, defaultColumns)
	case outputWide:
		return writeTable(writer, result.Jobs, result.FetchedAt, wideColumns)
	case outputJSON:
		encoder := json.NewEncoder(writer)
		if pretty {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(result)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func writeTable(writer io.Writer, jobs []finder.Job, fetchedAt time.Time, columns []tableColumn) error {
	var buffer bytes.Buffer
	table := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	headings := make([]string, len(columns))
	for index, column := range columns {
		headings[index] = column.header
	}
	if _, err := fmt.Fprintln(table, strings.Join(headings, "\t")); err != nil {
		return err
	}
	for _, job := range jobs {
		values := make([]string, len(columns))
		for index, column := range columns {
			values[index] = tableValue(column.value(job, fetchedAt), column.width)
		}
		if _, err := fmt.Fprintln(table, strings.Join(values, "\t")); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err := io.Copy(writer, &buffer)
	return err
}

func tableValue(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		value = missingValue
	}
	if utf8.RuneCountInString(value) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(value)
	return string(runes[:width-1]) + "…"
}

func jobAge(job finder.Job, fetchedAt time.Time) string {
	if job.DatePosted == nil {
		return unknownAge
	}
	age := fetchedAt.Sub(*job.DatePosted)
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}

func jobTypes(job finder.Job, _ time.Time) string {
	if len(job.JobTypes) == 0 {
		return missingValue
	}
	values := make([]string, len(job.JobTypes))
	for index, jobType := range job.JobTypes {
		values[index] = string(jobType)
	}
	return strings.Join(values, ",")
}

func jobSalary(job finder.Job, _ time.Time) string {
	if job.Compensation == nil {
		return missingValue
	}
	compensation := job.Compensation
	amount := ""
	switch {
	case compensation.MinAmount != 0 && compensation.MaxAmount != 0:
		amount = formatAmount(compensation.MinAmount) + "-" + formatAmount(compensation.MaxAmount)
	case compensation.MinAmount != 0:
		amount = formatAmount(compensation.MinAmount) + "+"
	case compensation.MaxAmount != 0:
		amount = "up to " + formatAmount(compensation.MaxAmount)
	default:
		return missingValue
	}
	parts := make([]string, 0, 3)
	if compensation.Currency != "" {
		parts = append(parts, compensation.Currency)
	}
	parts = append(parts, amount)
	value := strings.Join(parts, " ")
	if compensation.Interval != "" {
		value += "/" + string(compensation.Interval)
	}
	return value
}

func formatAmount(amount float64) string {
	return strconv.FormatFloat(amount, 'f', -1, 64)
}
