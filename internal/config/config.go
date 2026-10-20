// Package config loads settings from environment variables (12-factor).
// See .env.example for every key and its default.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config is the full service configuration.
type Config struct {
	HTTPAddr string
	LogLevel string

	RPC        RPCConfig
	Holders    HoldersConfig
	Cache      CacheConfig
	Limits     LimitsConfig
	Price      PriceConfig
	ClickHouse ClickHouseConfig
}

// RPCConfig configures the Solana RPC client.
type RPCConfig struct {
	URL            string // full URL; Helius keys go in ?api-key=
	Timeout        time.Duration
	MaxRetries     int
	MaxConcurrency int
	UseGPAV2       bool // Helius paginated getProgramAccountsV2
	UseDAS         bool // DAS getAssetBatch for NFT owners (Helius)
}

// HoldersConfig bounds the holder scans.
type HoldersConfig struct {
	MaxPositions           int // refuse on-demand scans above this many positions
	NFTFallbackConcurrency int
}

// CacheConfig configures the in-process snapshot cache.
type CacheConfig struct {
	TTL            time.Duration
	NegativeTTL    time.Duration
	MaxEntries     int
	MaxHolders     int // total holder rows kept across cached snapshots (memory bound)
	ComputeTimeout time.Duration
}

// LimitsConfig protects the service from expensive request patterns.
type LimitsConfig struct {
	// MaxConcurrentComputes bounds cold-cache computations running at once;
	// extra cold requests get 503 instead of queueing on the RPC.
	MaxConcurrentComputes int
}

// PriceConfig configures the optional USD price source.
type PriceConfig struct {
	BaseURL string
	Path    string
	APIKey  string
}

// Enabled reports whether a price source is configured.
func (p PriceConfig) Enabled() bool { return p.BaseURL != "" }

// ClickHouseConfig configures the optional LP holder index (token-holder's
// wallet_info.token_holder_local). Shards are routed with cityHash64(lp_mint) % N.
type ClickHouseConfig struct {
	// Shards lists replicas per shard in shard order: "h1:9000|h2:9000,h3:9000|h4:9000".
	Shards   [][]string
	User     string
	Password string
	Table    string
	MaxRows  int
}

// Enabled reports whether the ClickHouse LP source is configured.
func (c ClickHouseConfig) Enabled() bool { return len(c.Shards) > 0 }

var tableName = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom reads the configuration through getenv (injectable for tests).
func LoadFrom(getenv func(string) string) (Config, error) {
	e := env{get: getenv}
	cfg := Config{
		HTTPAddr: e.str("LP_HTTP_ADDR", ":8080"),
		LogLevel: e.str("LP_LOG_LEVEL", "info"),
		RPC: RPCConfig{
			URL:            e.str("LP_RPC_URL", ""),
			Timeout:        e.dur("LP_RPC_TIMEOUT", 60*time.Second),
			MaxRetries:     e.int("LP_RPC_MAX_RETRIES", 3),
			MaxConcurrency: e.int("LP_RPC_MAX_CONCURRENCY", 8),
			UseGPAV2:       e.bool("LP_RPC_USE_GPA_V2", false),
			UseDAS:         e.bool("LP_RPC_USE_DAS", true),
		},
		Holders: HoldersConfig{
			MaxPositions:           e.int("LP_MAX_POSITIONS", 50_000),
			NFTFallbackConcurrency: e.int("LP_NFT_FALLBACK_CONCURRENCY", 8),
		},
		Cache: CacheConfig{
			TTL:            e.dur("LP_CACHE_TTL", 60*time.Second),
			NegativeTTL:    e.dur("LP_NEGATIVE_CACHE_TTL", 5*time.Minute),
			MaxEntries:     e.int("LP_CACHE_MAX_ENTRIES", 256),
			MaxHolders:     e.int("LP_CACHE_MAX_HOLDERS", 1_000_000),
			ComputeTimeout: e.dur("LP_COMPUTE_TIMEOUT", 2*time.Minute),
		},
		Limits: LimitsConfig{
			MaxConcurrentComputes: e.int("LP_MAX_CONCURRENT_COMPUTES", 4),
		},
		Price: PriceConfig{
			BaseURL: e.str("LP_PRICE_BASE_URL", ""),
			Path:    e.str("LP_PRICE_PATH", "/defi/multi_price"),
			APIKey:  e.str("LP_PRICE_API_KEY", ""),
		},
		ClickHouse: ClickHouseConfig{
			Shards:   parseShards(e.str("LP_CLICKHOUSE_SHARDS", "")),
			User:     e.str("LP_CLICKHOUSE_USER", "default"),
			Password: e.str("LP_CLICKHOUSE_PASSWORD", ""),
			Table:    e.str("LP_CLICKHOUSE_TABLE", "wallet_info.token_holder_local"),
			MaxRows:  e.int("LP_CLICKHOUSE_MAX_ROWS", 100_000),
		},
	}
	if len(e.errs) > 0 {
		return Config{}, errors.Join(e.errs...)
	}
	return cfg, cfg.Validate()
}

// Validate checks required values and bounds.
func (c Config) Validate() error {
	var errs []error
	if c.RPC.URL == "" {
		errs = append(errs, errors.New("LP_RPC_URL is required"))
	} else if !strings.HasPrefix(c.RPC.URL, "http://") && !strings.HasPrefix(c.RPC.URL, "https://") {
		errs = append(errs, errors.New("LP_RPC_URL must be an http(s) URL"))
	}
	if c.RPC.MaxConcurrency <= 0 {
		errs = append(errs, errors.New("LP_RPC_MAX_CONCURRENCY must be > 0"))
	}
	if c.RPC.MaxRetries < 0 {
		errs = append(errs, errors.New("LP_RPC_MAX_RETRIES must be >= 0"))
	}
	if c.Holders.MaxPositions < 0 {
		errs = append(errs, errors.New("LP_MAX_POSITIONS must be >= 0 (0 disables the limit)"))
	}
	if c.Cache.ComputeTimeout <= 0 {
		errs = append(errs, errors.New("LP_COMPUTE_TIMEOUT must be > 0"))
	}
	if c.Cache.TTL < 0 || c.Cache.NegativeTTL < 0 {
		errs = append(errs, errors.New("LP_CACHE_TTL and LP_NEGATIVE_CACHE_TTL must be >= 0"))
	}
	if c.Cache.MaxEntries <= 0 || c.Cache.MaxHolders <= 0 {
		errs = append(errs, errors.New("LP_CACHE_MAX_ENTRIES and LP_CACHE_MAX_HOLDERS must be > 0"))
	}
	if c.Limits.MaxConcurrentComputes <= 0 {
		errs = append(errs, errors.New("LP_MAX_CONCURRENT_COMPUTES must be > 0"))
	}
	if c.ClickHouse.Enabled() {
		if !tableName.MatchString(c.ClickHouse.Table) {
			errs = append(errs, fmt.Errorf("LP_CLICKHOUSE_TABLE %q must look like db.table", c.ClickHouse.Table))
		}
		if c.ClickHouse.MaxRows <= 0 {
			errs = append(errs, errors.New("LP_CLICKHOUSE_MAX_ROWS must be > 0"))
		}
	}
	return errors.Join(errs...)
}

func parseShards(v string) [][]string {
	var shards [][]string
	for _, shard := range strings.Split(v, ",") {
		var replicas []string
		for _, r := range strings.Split(shard, "|") {
			if r = strings.TrimSpace(r); r != "" {
				replicas = append(replicas, r)
			}
		}
		if len(replicas) > 0 {
			shards = append(shards, replicas)
		}
	}
	return shards
}

type env struct {
	get  func(string) string
	errs []error
}

func (e *env) str(key, def string) string {
	if v := strings.TrimSpace(e.get(key)); v != "" {
		return v
	}
	return def
}

func (e *env) int(key string, def int) int {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (e *env) bool(key string, def bool) bool {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid boolean %q", key, v))
		return def
	}
	return b
}

func (e *env) dur(key string, def time.Duration) time.Duration {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}
