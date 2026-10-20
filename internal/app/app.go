// Package app wires configuration into a ready-to-use service.
package app

import (
	"fmt"
	"log/slog"
	"time"

	"birdeye.so/lp-holder/internal/config"
	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/holders"
	"birdeye.so/lp-holder/internal/lpsource"
	"birdeye.so/lp-holder/internal/price"
	"birdeye.so/lp-holder/internal/protocol"
	"birdeye.so/lp-holder/internal/service"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// clickHouseTimeout bounds the ClickHouse LP lookup before falling back to RPC.
const clickHouseTimeout = 10 * time.Second

// App holds the service and resources to release on shutdown.
type App struct {
	Service *service.Service
	closers []func()
}

// Close releases resources.
func (a *App) Close() {
	for _, c := range a.closers {
		c()
	}
}

// New builds the application from cfg.
func New(cfg config.Config) (*App, error) {
	client := rpc.New(rpc.Config{
		Endpoint:       cfg.RPC.URL,
		Timeout:        cfg.RPC.Timeout,
		MaxRetries:     cfg.RPC.MaxRetries,
		MaxConcurrency: cfg.RPC.MaxConcurrency,
	})
	scanner := holders.RPCScanner{Client: client, UseV2: cfg.RPC.UseGPAV2}
	owners := holders.NewNFTOwnerResolver(client, cfg.RPC.UseDAS, cfg.Holders.NFTFallbackConcurrency)

	a := &App{}
	var lpSource holders.LPHolderSource = holders.RPCLPSource{Scanner: scanner}
	if cfg.ClickHouse.Enabled() {
		ch, err := lpsource.NewClickHouse(cfg.ClickHouse)
		if err != nil {
			return nil, fmt.Errorf("init clickhouse lp source: %w", err)
		}
		a.closers = append(a.closers, ch.Close)
		lpSource = holders.FallbackLPSource{Primary: ch, Secondary: lpSource, PrimaryTimeout: clickHouseTimeout}
		slog.Info("LP holders: ClickHouse index with RPC fallback", "shards", len(cfg.ClickHouse.Shards))
	}

	maxPos := cfg.Holders.MaxPositions
	lp := holders.LPTokenStrategy{Source: lpSource, MaxHolders: maxPos}
	strategies := map[domain.Protocol]holders.Strategy{
		domain.ProtocolRaydiumAMMV4:  lp,
		domain.ProtocolRaydiumCPMM:   lp,
		domain.ProtocolLifinityV2:    lp,
		domain.ProtocolMeteoraDAMMV1: lp,
		domain.ProtocolRaydiumCLMM: holders.TickStrategy{
			Adapter: protocol.RaydiumCLMM{}, Scanner: scanner, Owners: owners, MaxPositions: maxPos,
		},
		domain.ProtocolOrcaWhirlpool: holders.TickStrategy{
			Adapter: protocol.OrcaWhirlpool{}, Scanner: scanner, Owners: owners, MaxPositions: maxPos,
		},
		domain.ProtocolMeteoraDAMMV2: holders.DAMMV2Strategy{Scanner: scanner, Owners: owners, MaxPositions: maxPos},
		domain.ProtocolMeteoraDLMM:   holders.DLMMStrategy{Scanner: scanner, Accounts: client, MaxPositions: maxPos},
	}

	var prices service.PriceProvider
	if cfg.Price.Enabled() {
		prices = price.New(price.Config{BaseURL: cfg.Price.BaseURL, Path: cfg.Price.Path, APIKey: cfg.Price.APIKey})
	}

	a.Service = service.New(service.Config{
		CacheTTL:              cfg.Cache.TTL,
		NegativeCacheTTL:      cfg.Cache.NegativeTTL,
		ComputeTimeout:        cfg.Cache.ComputeTimeout,
		CacheMaxEntries:       cfg.Cache.MaxEntries,
		CacheMaxHolders:       cfg.Cache.MaxHolders,
		MaxConcurrentComputes: cfg.Limits.MaxConcurrentComputes,
	}, service.Deps{
		RPC:        client,
		Registry:   protocol.DefaultRegistry(),
		Strategies: strategies,
		Prices:     prices,
	})
	return a, nil
}
