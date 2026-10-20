// Package price fetches USD token prices from a Birdeye-compatible
// multi_price endpoint (public API /defi/multi_price or the internal
// birdeye_backend /public/multi_price — both return the same shape).
package price

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 5 * time.Second
	maxErrorBody   = 256
)

// Config configures a Client.
type Config struct {
	BaseURL string // e.g. https://public-api.birdeye.so
	Path    string // e.g. /defi/multi_price
	APIKey  string // sent as X-API-KEY when set
	Chain   string // sent as x-chain, default "solana"
	Timeout time.Duration
}

// Client implements service.PriceProvider.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates a price Client.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Chain == "" {
		cfg.Chain = "solana"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

type priceEntry struct {
	Value float64 `json:"value"`
}

type multiPriceResponse struct {
	Success bool                   `json:"success"`
	Data    map[string]*priceEntry `json:"data"`
}

// Prices returns mint -> USD price. Unknown mints are omitted.
func (c *Client) Prices(ctx context.Context, mints []string) (map[string]float64, error) {
	q := url.Values{"list_address": {strings.Join(mints, ",")}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+c.cfg.Path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build price request: %w", err)
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("x-chain", c.cfg.Chain)
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-KEY", c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("price request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("price api http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded multiPriceResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode price response: %w", err)
	}
	if !decoded.Success {
		return nil, fmt.Errorf("price api returned success=false")
	}
	out := make(map[string]float64, len(decoded.Data))
	for mint, p := range decoded.Data {
		if p != nil && p.Value > 0 {
			out[mint] = p.Value
		}
	}
	return out, nil
}
