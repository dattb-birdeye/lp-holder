// Package domain defines the pool/holder model shared by decoders,
// holder strategies, the service layer and the HTTP API.
package domain

import (
	"errors"
	"math/big"
	"time"
)

// Protocol identifies a DEX program.
type Protocol string

const (
	ProtocolRaydiumAMMV4  Protocol = "raydium_amm_v4"
	ProtocolRaydiumCPMM   Protocol = "raydium_cpmm"
	ProtocolRaydiumCLMM   Protocol = "raydium_clmm"
	ProtocolOrcaWhirlpool Protocol = "orca_whirlpool"
	ProtocolMeteoraDLMM   Protocol = "meteora_dlmm"
	ProtocolMeteoraDAMMV1 Protocol = "meteora_damm_v1"
	ProtocolMeteoraDAMMV2 Protocol = "meteora_damm_v2"
	ProtocolLifinityV2    Protocol = "lifinity_amm_v2"
)

// Kind is how liquidity ownership is represented on-chain.
type Kind string

const (
	// KindLPToken: a fungible LP mint; holders are LP token holders.
	KindLPToken Kind = "lp_token"
	// KindPosition: one account per position (tick range, bin range or NFT).
	KindPosition Kind = "position"
)

// Pool is the decoded pool state plus enrichment (decimals, reserves).
type Pool struct {
	Address  string
	Protocol Protocol
	Kind     Kind

	MintA  string
	MintB  string
	VaultA string
	VaultB string

	// LP-token pools.
	LPMint string
	// LPSupply is the supply used by the protocol's withdraw math. Taken from
	// pool state when the program tracks it (AMM v4 lp_amount, CPMM lp_supply),
	// otherwise from the LP mint account.
	LPSupply *big.Int
	// LPMintSupply is the LP mint's current supply. It is lower than LPSupply
	// when LP tokens were burned (common for launchpad-migrated pools).
	LPMintSupply *big.Int

	// Concentrated pools (Q64.64 sqrt prices).
	SqrtPriceX64    *big.Int
	SqrtMinPriceX64 *big.Int // DAMM v2 only
	SqrtMaxPriceX64 *big.Int // DAMM v2 only
	TickCurrent     *int32

	// DLMM.
	ActiveBinID *int32
	BinStep     *uint16

	Reserve ReserveSpec

	// Enrichment (filled by the service).
	DecimalsA  uint8
	DecimalsB  uint8
	ReserveA   *big.Int
	ReserveB   *big.Int
	PriceAInB  float64 // UI units: how many B for 1 A
	LPTokenPgm string  // token program owning the LP mint
}

// ReserveSpec tells the service how to turn vault balances into reserves.
type ReserveSpec struct {
	// Amounts sitting in the vaults that do not belong to LPs (protocol/fund/
	// creator fees, pending PnL). Subtracted from the vault balances.
	ExcludeA uint64
	ExcludeB uint64

	// Meteora DAMM v1 does not hold tokens itself: it holds LP shares of shared
	// Meteora vaults. reserve = vaultLPBalance * vault.total_amount / vaultLPSupply.
	MeteoraVaultA   string
	MeteoraVaultB   string
	VaultLPAccountA string
	VaultLPAccountB string
}

// Holder is one LP position (or LP token balance) in a pool.
type Holder struct {
	Wallet          string
	PositionAddress string
	Liquidity       *big.Int // LP balance or position liquidity
	AmountARaw      *big.Int
	AmountBRaw      *big.Int
	AmountA         float64
	AmountB         float64
	ValueInB        float64
	Percentage      float64
	ValueUSD        float64
	PositionCount   int
	TickLower       *int32
	TickUpper       *int32
	BinIDLower      *int32
	BinIDUpper      *int32
}

// Snapshot is a fully valued holder list for one pool at one point in time.
type Snapshot struct {
	Pool        *Pool
	Holders     []Holder // sorted by percentage desc
	TotalUSD    float64
	HasUSDPrice bool
	FetchedAt   time.Time
	Warnings    []string
	// Incomplete marks snapshots with unresolved owners or skipped positions.
	Incomplete bool
}

// Errors shared across layers.
var (
	ErrInvalidAddress  = errors.New("invalid pool address")
	ErrPoolNotFound    = errors.New("pool account not found")
	ErrUnsupportedPool = errors.New("unsupported pool program or account type")
	ErrPoolTooLarge    = errors.New("pool has too many positions for on-demand computation")
	ErrBusy            = errors.New("too many pools are being computed; retry shortly")
)
