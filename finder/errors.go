package finder

import (
	"errors"
	"fmt"
)

// ErrRateLimited identifies a LinkedIn HTTP 429 response.
var ErrRateLimited = errors.New("linkedin rate limited")

// SearchError describes a failed stage of a LinkedIn search. A Search call
// may return both jobs and a SearchError when work completed before failure.
type SearchError struct {
	Operation  string
	URL        string
	StatusCode int
	Err        error
}

func (e *SearchError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "linkedin " + e.Operation + " failed"
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" with status %d", e.StatusCode)
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

// Unwrap exposes the underlying transport, context, parsing, or rate-limit
// error for errors.Is and errors.As.
func (e *SearchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
