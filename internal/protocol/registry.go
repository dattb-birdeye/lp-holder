// Package protocol decodes pool and position accounts for each supported DEX.
//
// Every offset in this package was verified against mainnet accounts (see
// testdata/ and README "Layout verification"). Decoders are pure functions:
// they never call RPC, which keeps them unit-testable with fixtures.
package protocol

import (
	"fmt"
	"math/big"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/solana"
	"birdeye.so/lp-holder/internal/solana/rpc"
)

// Adapter decodes the pool account of one protocol.
type Adapter interface {
	Protocol() domain.Protocol
	ProgramID() string
	// Match reports whether data (owned by ProgramID) is this protocol's pool account.
	Match(data []byte) bool
	DecodePool(address string, data []byte) (*domain.Pool, error)
}

// TickPosition is a concentrated-liquidity position whose owner is the holder
// of an NFT (Raydium CLMM, Orca Whirlpool).
type TickPosition struct {
	NFTMint   string
	TickLower int32
	TickUpper int32
	Liquidity *big.Int
}

// TickAdapter is implemented by tick-based protocols.
type TickAdapter interface {
	Adapter
	// PositionQuery returns the getProgramAccounts scan for all positions of pool,
	// including a dataSlice so only the needed bytes are transferred.
	PositionQuery(pool string) rpc.ProgramAccountsQuery
	DecodePositionSlice(slice []byte) (TickPosition, error)
}

// Registry maps program IDs to adapters.
type Registry struct {
	byProgram map[string][]Adapter
}

// NewRegistry builds a registry from adapters.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{byProgram: make(map[string][]Adapter)}
	for _, a := range adapters {
		r.byProgram[a.ProgramID()] = append(r.byProgram[a.ProgramID()], a)
	}
	return r
}

// DefaultRegistry contains every supported protocol.
func DefaultRegistry() *Registry {
	return NewRegistry(
		RaydiumAMMV4{},
		RaydiumCPMM{},
		LifinityV2{},
		MeteoraDAMMV1{},
		RaydiumCLMM{},
		OrcaWhirlpool{},
		MeteoraDLMM{},
		MeteoraDAMMV2{},
	)
}

// Detect returns the adapter for an account owned by owner.
func (r *Registry) Detect(owner string, data []byte) (Adapter, error) {
	for _, a := range r.byProgram[owner] {
		if a.Match(data) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%w: owner=%s size=%d", domain.ErrUnsupportedPool, owner, len(data))
}

// SupportedPrograms lists the registered program IDs.
func (r *Registry) SupportedPrograms() []string {
	out := make([]string, 0, len(r.byProgram))
	for p := range r.byProgram {
		out = append(out, p)
	}
	return out
}

func newLayout(data []byte, minSize int, what string) (solana.Layout, error) {
	l := solana.NewLayout(data)
	if err := l.Require(minSize); err != nil {
		return l, fmt.Errorf("decode %s: %w", what, err)
	}
	return l, nil
}

func ptr[T any](v T) *T { return &v }

func errNotAccount(what string) error {
	return fmt.Errorf("decode %s: discriminator mismatch", what)
}

// sliceQuery is a helper to build a dataSlice pointer.
func sliceQuery(offset, length int) *rpc.DataSlice {
	return &rpc.DataSlice{Offset: offset, Length: length}
}
