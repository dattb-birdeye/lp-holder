// Package rpc is a small Solana JSON-RPC client tuned for heavy read paths:
// bounded concurrency, retry with backoff on 429/5xx, and error messages that
// never contain the endpoint URL (Helius puts the API key in the query string).
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	defaultTimeout        = 60 * time.Second
	defaultMaxConcurrency = 8
	defaultBackoffBase    = 300 * time.Millisecond
	defaultBackoffMax     = 5 * time.Second
	maxErrorBodyBytes     = 256
)

// JSON-RPC error codes that are worth retrying.
const (
	codeNodeUnhealthy = -32005
	codeRateLimited   = -32429
)

// Config configures a Client.
type Config struct {
	Endpoint       string        // full RPC URL, may contain ?api-key=
	Timeout        time.Duration // per attempt
	MaxRetries     int           // extra attempts after the first one (0 = no retry)
	MaxConcurrency int           // in-flight requests across all methods
	BackoffBase    time.Duration
	BackoffMax     time.Duration
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	c.MaxRetries = max(c.MaxRetries, 0)
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = defaultMaxConcurrency
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = defaultBackoffBase
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = defaultBackoffMax
	}
	return c
}

// Client executes JSON-RPC calls.
type Client struct {
	cfg  Config
	http *http.Client
	sem  chan struct{}
	ids  atomic.Uint64
}

// New creates a Client.
func New(cfg Config) *Client {
	cfg = cfg.withDefaults()
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		sem:  make(chan struct{}, cfg.MaxConcurrency),
	}
}

// Error is a JSON-RPC level error returned by the node.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// IsMethodNotFound reports whether err means the endpoint lacks the method
// (e.g. DAS methods on a non-Helius node).
func IsMethodNotFound(err error) bool {
	var rpcErr *Error
	return errors.As(err, &rpcErr) && rpcErr.Code == -32601
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// retryableError marks failures that may succeed on a later attempt.
type retryableError struct {
	err        error
	retryAfter time.Duration
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

// Call executes method with params and decodes the result into out.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: c.ids.Add(1), Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("%s: marshal request: %w", method, err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.backoff(attempt, lastErr)); err != nil {
				return err
			}
		}
		raw, err := c.attempt(ctx, body)
		if err == nil {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("%s: decode result: %w", method, err)
			}
			return nil
		}
		lastErr = err
		var re *retryableError
		if !errors.As(err, &re) || ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("%s: %w", method, lastErr)
}

// attempt holds a concurrency slot for one HTTP round trip only, so slots are
// not wasted while a retry is backing off.
func (c *Client) attempt(ctx context.Context, body []byte) (json.RawMessage, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	return c.do(ctx, body)
}

func (c *Client) do(ctx context.Context, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build request failed")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &retryableError{err: stripURL(err)}
	}
	defer func() {
		// Drain so the keep-alive connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		statusErr := fmt.Errorf("http %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			return nil, &retryableError{err: statusErr, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return nil, statusErr
	}

	var decoded response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, &retryableError{err: fmt.Errorf("decode response: %w", err)}
	}
	if decoded.Error != nil {
		if decoded.Error.Code == codeNodeUnhealthy || decoded.Error.Code == codeRateLimited {
			return nil, &retryableError{err: decoded.Error}
		}
		return nil, decoded.Error
	}
	return decoded.Result, nil
}

func (c *Client) backoff(attempt int, lastErr error) time.Duration {
	var re *retryableError
	if errors.As(lastErr, &re) && re.retryAfter > 0 {
		return min(re.retryAfter, c.cfg.BackoffMax)
	}
	d := c.cfg.BackoffBase << (attempt - 1)
	if d > c.cfg.BackoffMax || d <= 0 {
		d = c.cfg.BackoffMax
	}
	// Full jitter keeps concurrent retries from hitting the node in lockstep.
	return time.Duration(rand.Int64N(int64(d)) + 1)
}

// stripURL drops the request URL from net/http errors so API keys never leak into logs.
func stripURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("http request failed: %w", urlErr.Err)
	}
	return err
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
