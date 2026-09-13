package finder

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	linkedinBaseURL  = "https://www.linkedin.com"
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var retryableStatuses = map[int]bool{
	http.StatusTooManyRequests:     true,
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true,
	http.StatusGatewayTimeout:      true,
}

// Client searches LinkedIn's public guest job pages. It is safe for
// concurrent searches as long as the injected http.Client is also safe.
type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string

	searchTimeout time.Duration
	detailTimeout time.Duration
	minPageDelay  time.Duration
	maxPageDelay  time.Duration
	maxRetries    int
	retryBackoff  time.Duration
	sleep         func(context.Context, time.Duration) error
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelReadCloser) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

// NewClient constructs a LinkedIn finder client.
func NewClient(config ClientConfig) *Client {
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	userAgent := strings.TrimSpace(config.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Client{
		httpClient:    httpClient,
		userAgent:     userAgent,
		baseURL:       linkedinBaseURL,
		searchTimeout: 10 * time.Second,
		detailTimeout: 5 * time.Second,
		minPageDelay:  3 * time.Second,
		maxPageDelay:  7 * time.Second,
		maxRetries:    3,
		retryBackoff:  5 * time.Second,
		sleep:         sleepContext,
	}
}

func (c *Client) get(ctx context.Context, rawURL, operation string, timeout time.Duration) (*http.Response, error) {
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, &SearchError{Operation: operation, URL: rawURL, Err: err}
		}

		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return nil, &SearchError{Operation: operation, URL: rawURL, Err: err}
		}
		c.setHeaders(req)
		resp, requestErr := c.httpClient.Do(req)

		if requestErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
			resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		}
		cancel()

		statusCode := 0
		retryAfter := time.Duration(0)
		if resp != nil {
			statusCode = resp.StatusCode
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			drainAndClose(resp.Body)
		}

		if err := ctx.Err(); err != nil {
			return nil, &SearchError{Operation: operation, URL: rawURL, StatusCode: statusCode, Err: err}
		}
		retryable := requestErr != nil || retryableStatuses[statusCode]
		if !retryable || attempt == c.maxRetries {
			cause := requestErr
			if statusCode == http.StatusTooManyRequests {
				cause = errors.Join(ErrRateLimited, requestErr)
			}
			return nil, &SearchError{
				Operation:  operation,
				URL:        rawURL,
				StatusCode: statusCode,
				Err:        cause,
			}
		}

		delay := c.retryBackoff * time.Duration(1<<attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, &SearchError{Operation: operation, URL: rawURL, StatusCode: statusCode, Err: err}
		}
	}
	panic("unreachable")
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("User-Agent", c.userAgent)
}

func (c *Client) pageDelay() time.Duration {
	if c.maxPageDelay <= c.minPageDelay {
		return c.minPageDelay
	}
	return c.minPageDelay + time.Duration(rand.Int64N(int64(c.maxPageDelay-c.minPageDelay)+1))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 32<<10))
	_ = body.Close()
}
