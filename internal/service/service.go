// Package service orchestrates holder discovery for one pool:
// detect protocol -> decode -> enrich -> list holders -> value -> cache.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/holders"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

const (
	defaultCacheTTL     = 60 * time.Second
	defaultNegativeTTL  = 5 * time.Minute
	defaultComputeLimit = 2 * time.Minute
	defaultMaxEntries   = 256
	defaultMaxHolders   = 1_000_000
	defaultMaxComputes  = 4
	incompleteTTL       = 15 * time.Second // retry unresolved owners soon, but not on every request
	priceTimeout        = 3 * time.Second
	defaultPageLimit    = 50
	maxPageLimit        = 500
)

// GroupBy selects the row granularity of a page.
type GroupBy string

const (
	GroupByPosition GroupBy = "position" // one row per position / LP owner
	GroupByWallet   GroupBy = "wallet"   // positions merged per wallet
)

// PoolRPC is the RPC subset used by the service.
type PoolRPC interface {
	GetAccountInfo(ctx context.Context, address string) (*rpc.Account, error)
	GetMultipleAccounts(ctx context.Context, addresses []string) ([]*rpc.Account, error)
}

// PriceProvider returns USD prices by mint. Missing mints are absent.
type PriceProvider interface {
	Prices(ctx context.Context, mints []string) (map[string]float64, error)
}

// Config tunes caching and limits.
type Config struct {
	CacheTTL              time.Duration // how long a computed snapshot is served
	NegativeCacheTTL      time.Duration // how long "not found / unsupported" is remembered
	ComputeTimeout        time.Duration // budget for one snapshot computation
	CacheMaxEntries       int
	CacheMaxHolders       int // total holder rows across cached snapshots
	MaxConcurrentComputes int // cold computations at once; more get ErrBusy
}

// Deps are the collaborators of the service.
type Deps struct {
	RPC        PoolRPC
	Registry   *protocol.Registry
	Strategies map[domain.Protocol]holders.Strategy
	Prices     PriceProvider // optional
	Now        func() time.Time
}

// Service computes and caches holder snapshots.
type Service struct {
	cfg          Config
	rpc          PoolRPC
	registry     *protocol.Registry
	strategies   map[domain.Protocol]holders.Strategy
	prices       PriceProvider
	now          func() time.Time
	snapshots    *ttlCache[*cachedSnapshot]
	negative     *ttlCache[error]
	inflight     singleflight.Group
	computeSlots chan struct{}
}

// cachedSnapshot is shared read-only by all requests; the wallet-grouped view
// is built once on first use.
type cachedSnapshot struct {
	snap     *domain.Snapshot
	once     sync.Once
	byWallet []domain.Holder
}

func (c *cachedSnapshot) rows(groupBy GroupBy) []domain.Holder {
	if groupBy != GroupByWallet {
		return c.snap.Holders
	}
	c.once.Do(func() { c.byWallet = groupByWallet(c.snap.Holders) })
	return c.byWallet
}

// New creates a Service.
func New(cfg Config, deps Deps) *Service {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	if cfg.NegativeCacheTTL <= 0 {
		cfg.NegativeCacheTTL = defaultNegativeTTL
	}
	if cfg.ComputeTimeout <= 0 {
		cfg.ComputeTimeout = defaultComputeLimit
	}
	if cfg.CacheMaxEntries <= 0 {
		cfg.CacheMaxEntries = defaultMaxEntries
	}
	if cfg.CacheMaxHolders <= 0 {
		cfg.CacheMaxHolders = defaultMaxHolders
	}
	if cfg.MaxConcurrentComputes <= 0 {
		cfg.MaxConcurrentComputes = defaultMaxComputes
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Service{
		cfg:          cfg,
		rpc:          deps.RPC,
		registry:     deps.Registry,
		strategies:   deps.Strategies,
		prices:       deps.Prices,
		now:          deps.Now,
		snapshots:    newTTLCache[*cachedSnapshot](cfg.CacheMaxEntries, cfg.CacheMaxHolders, deps.Now),
		negative:     newTTLCache[error](cfg.CacheMaxEntries*4, cfg.CacheMaxEntries*4, deps.Now),
		computeSlots: make(chan struct{}, cfg.MaxConcurrentComputes),
	}
}

// Query selects a page of holders.
type Query struct {
	Pool    string
	Offset  int
	Limit   int
	GroupBy GroupBy
}

// Page is one page of a snapshot.
type Page struct {
	Snapshot *domain.Snapshot
	Holders  []domain.Holder
	Total    int
	Offset   int
	Limit    int
	GroupBy  GroupBy
}

// Normalize applies defaults and bounds to a query.
func (q Query) Normalize() Query {
	if q.Limit <= 0 {
		q.Limit = defaultPageLimit
	}
	q.Limit = min(q.Limit, maxPageLimit)
	q.Offset = max(q.Offset, 0)
	if q.GroupBy != GroupByWallet {
		q.GroupBy = GroupByPosition
	}
	return q
}

// Holders returns a page of holders, computing the snapshot on a cache miss.
// Concurrent requests for the same pool share one computation.
func (s *Service) Holders(ctx context.Context, q Query) (*Page, error) {
	q = q.Normalize()
	if !solana.IsValidPubkey(q.Pool) {
		return nil, domain.ErrInvalidAddress
	}
	cs, err := s.cached(ctx, q.Pool)
	if err != nil {
		return nil, err
	}
	rows := cs.rows(q.GroupBy)
	start := min(q.Offset, len(rows))
	end := min(start+q.Limit, len(rows))
	return &Page{
		Snapshot: cs.snap,
		// Capacity is capped so an append by a caller cannot write into the shared snapshot.
		Holders: rows[start:end:end],
		Total:   len(rows),
		Offset:  q.Offset,
		Limit:   q.Limit,
		GroupBy: q.GroupBy,
	}, nil
}

// Snapshot returns the cached snapshot of pool or computes it.
func (s *Service) Snapshot(ctx context.Context, pool string) (*domain.Snapshot, error) {
	cs, err := s.cached(ctx, pool)
	if err != nil {
		return nil, err
	}
	return cs.snap, nil
}

func (s *Service) cached(ctx context.Context, pool string) (*cachedSnapshot, error) {
	if cs, ok := s.snapshots.get(pool); ok {
		return cs, nil
	}
	if err, ok := s.negative.get(pool); ok {
		return nil, err
	}
	// The computation is detached from the caller: a client disconnect must not
	// abort a scan other callers are waiting for, and the result is cached.
	ch := s.inflight.DoChan(pool, func() (any, error) {
		return s.computeAndCache(context.WithoutCancel(ctx), pool)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*cachedSnapshot), nil
	}
}

// computeAndCache runs inside singleflight's goroutine, so it must never panic:
// a panic there would crash the process (net/http cannot recover it).
func (s *Service) computeAndCache(ctx context.Context, pool string) (cs *cachedSnapshot, err error) {
	select {
	case s.computeSlots <- struct{}{}:
	default:
		return nil, domain.ErrBusy
	}
	defer func() { <-s.computeSlots }()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic while computing pool holders", "pool", pool, "panic", r, "stack", string(debug.Stack()))
			cs, err = nil, fmt.Errorf("compute pool %s: internal error", pool)
		}
	}()

	cctx, cancel := context.WithTimeout(ctx, s.cfg.ComputeTimeout)
	defer cancel()
	snap, err := s.compute(cctx, pool)
	if err != nil {
		if isPermanent(err) && cctx.Err() == nil {
			s.negative.set(pool, err, s.cfg.NegativeCacheTTL, 1)
		}
		return nil, err
	}
	if cctx.Err() != nil {
		// Some sub-steps tolerate errors; a result produced while the budget ran
		// out may be partial, so it is neither served nor cached.
		return nil, cctx.Err()
	}
	cs = &cachedSnapshot{snap: snap}
	ttl := s.cfg.CacheTTL
	if snap.Incomplete {
		ttl = min(ttl, incompleteTTL)
	}
	s.snapshots.set(pool, cs, ttl, max(len(snap.Holders), 1))
	return cs, nil
}

func (s *Service) compute(ctx context.Context, address string) (*domain.Snapshot, error) {
	started := s.now()
	acct, err := s.rpc.GetAccountInfo(ctx, address)
	if errors.Is(err, rpc.ErrAccountNotFound) {
		return nil, domain.ErrPoolNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("fetch pool account: %w", err)
	}
	adapter, err := s.registry.Detect(acct.Owner, acct.Data)
	if err != nil {
		return nil, err
	}
	pool, err := adapter.DecodePool(address, acct.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrUnsupportedPool, err)
	}
	warnings, err := s.enrich(ctx, pool)
	if err != nil {
		return nil, err
	}
	strategy, ok := s.strategies[pool.Protocol]
	if !ok {
		return nil, fmt.Errorf("%w: no holder strategy for %s", domain.ErrUnsupportedPool, pool.Protocol)
	}
	res, err := strategy.Holders(ctx, pool)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, res.Warnings...)

	usdPerB := s.usdPerB(ctx, pool, &warnings)
	totalUSD := value(pool, res.Holders, usdPerB)

	slog.Info("pool holders computed",
		"pool", address, "protocol", pool.Protocol, "holders", len(res.Holders),
		"duration_ms", s.now().Sub(started).Milliseconds(), "warnings", len(warnings))
	return &domain.Snapshot{
		Pool:        pool,
		Holders:     res.Holders,
		TotalUSD:    totalUSD,
		HasUSDPrice: usdPerB > 0,
		FetchedAt:   s.now(),
		Warnings:    warnings,
		Incomplete:  res.Incomplete,
	}, nil
}

func (s *Service) usdPerB(ctx context.Context, pool *domain.Pool, warnings *[]string) float64 {
	if s.prices == nil {
		return 0
	}
	pctx, cancel := context.WithTimeout(ctx, priceTimeout)
	defer cancel()
	prices, err := s.prices.Prices(pctx, []string{pool.MintA, pool.MintB})
	if err != nil {
		*warnings = append(*warnings, "token prices unavailable; value_usd is 0")
		slog.Warn("fetch token prices failed", "pool", pool.Address, "error", err)
		return 0
	}
	return usdPerTokenB(pool, prices)
}

// isPermanent reports errors that will not change on retry soon.
func isPermanent(err error) bool {
	return errors.Is(err, domain.ErrPoolNotFound) ||
		errors.Is(err, domain.ErrUnsupportedPool) ||
		errors.Is(err, domain.ErrPoolTooLarge)
}
