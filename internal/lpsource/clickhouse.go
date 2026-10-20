// Package lpsource lists LP token holders from token-holder's existing
// ClickHouse index (wallet_info.token_holder_local) instead of scanning the
// token program over RPC. LP mints are ordinary SPL mints, so no new indexer
// is needed.
package lpsource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/cityhash102"

	"birdeye.so/lp-holder/internal/config"
	"birdeye.so/lp-holder/internal/holders"
)

const (
	dialTimeout = 5 * time.Second
	readTimeout = 15 * time.Second
)

// ErrTooManyHolders means the mint has more holders than MaxRows; callers fall
// back to RPC rather than serve a silently truncated list.
var ErrTooManyHolders = errors.New("lp mint has more holders than LP_CLICKHOUSE_MAX_ROWS")

// ClickHouse implements holders.LPHolderSource.
//
// token_holder_local is sharded by cityHash64(token_address), the same rule as
// token-holder's ShardManager.GetConnByShardingKey, so each query goes to the
// one shard that owns the mint (reading a _local table on a random shard was a
// bug in the previous branches).
type ClickHouse struct {
	shards  []clickhouse.Conn
	table   string
	maxRows int
}

// NewClickHouse opens one connection pool per shard.
func NewClickHouse(cfg config.ClickHouseConfig) (*ClickHouse, error) {
	c := &ClickHouse{table: cfg.Table, maxRows: cfg.MaxRows}
	for i, replicas := range cfg.Shards {
		conn, err := clickhouse.Open(&clickhouse.Options{
			Addr:             replicas,
			Auth:             clickhouse.Auth{Username: cfg.User, Password: cfg.Password},
			ConnOpenStrategy: clickhouse.ConnOpenRoundRobin,
			DialTimeout:      dialTimeout,
			ReadTimeout:      readTimeout,
		})
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("open clickhouse shard %d: %w", i, err)
		}
		c.shards = append(c.shards, conn)
	}
	return c, nil
}

// Close releases all connections.
func (c *ClickHouse) Close() {
	for _, conn := range c.shards {
		_ = conn.Close()
	}
}

// ShardFor returns the shard index owning key (cityHash64(key) % shards).
func ShardFor(key string, shards int) int {
	h := cityhash102.CityHash64([]byte(key), uint32(len(key)))
	return int(h % uint64(shards))
}

// LPBalances implements holders.LPHolderSource. tokenProgram is unused: the
// index covers both token programs.
func (c *ClickHouse) LPBalances(ctx context.Context, lpMint, _ string) ([]holders.LPBalance, error) {
	conn := c.shards[ShardFor(lpMint, len(c.shards))]
	// Latest balance per token account (holder_key), then summed per owner.
	query := fmt.Sprintf(`
		SELECT owner, toUInt64(sum(balance)) AS amount
		FROM (
			SELECT argMax(post_balance, version) AS balance, argMax(owner, version) AS owner
			FROM %s
			WHERE token_address = ?
			GROUP BY holder_key
			HAVING balance > 0
		)
		GROUP BY owner
		ORDER BY amount DESC
		LIMIT ?`, c.table) // table is validated as db.table by config.Validate

	rows, err := conn.Query(ctx, query, lpMint, uint64(c.maxRows)+1)
	if err != nil {
		return nil, fmt.Errorf("query lp holders: %w", err)
	}
	defer rows.Close()

	var out []holders.LPBalance
	for rows.Next() {
		var b holders.LPBalance
		if err := rows.Scan(&b.Owner, &b.Amount); err != nil {
			return nil, fmt.Errorf("scan lp holder: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > c.maxRows {
		return nil, ErrTooManyHolders
	}
	return out, nil
}
