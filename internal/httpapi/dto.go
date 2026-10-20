package httpapi

import (
	"math"
	"math/big"
	"strconv"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/poolmath"
	"birdeye.so/lp-holder/internal/service"
)

// Request is the body of POST /v1/holder/liquidity-provider.
// "pool_address" is accepted as an alias of "address" for old clients.
type Request struct {
	Address     string `json:"address"`
	PoolAddress string `json:"pool_address"`
	Offset      int    `json:"offset"`
	Limit       int    `json:"limit"`
	GroupBy     string `json:"group_by"` // "position" (default) | "wallet"
}

// Envelope matches token-holder's response shape.
type Envelope struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// PageResponse is the success payload.
type PageResponse struct {
	Pool      PoolResponse     `json:"pool"`
	Holders   []HolderResponse `json:"holders"`
	Total     int              `json:"total"`
	Offset    int              `json:"offset"`
	Limit     int              `json:"limit"`
	GroupBy   string           `json:"group_by"`
	UpdatedAt int64            `json:"updated_at"`
	Warnings  []string         `json:"warnings,omitempty"`
}

// PoolResponse describes the pool. Amounts are decimal strings to keep precision.
type PoolResponse struct {
	Address       string  `json:"address"`
	Protocol      string  `json:"protocol"`
	Kind          string  `json:"kind"`
	TokenMintA    string  `json:"token_mint_a"`
	TokenMintB    string  `json:"token_mint_b"`
	TokenVaultA   string  `json:"token_vault_a"`
	TokenVaultB   string  `json:"token_vault_b"`
	LPMint        string  `json:"lp_mint,omitempty"`
	LPSupply      string  `json:"lp_supply,omitempty"`
	LPMintSupply  string  `json:"lp_mint_supply,omitempty"`
	DecimalsA     uint8   `json:"decimals_a"`
	DecimalsB     uint8   `json:"decimals_b"`
	ReserveA      string  `json:"reserve_a"`
	ReserveB      string  `json:"reserve_b"`
	PriceAInB     string  `json:"price_a_in_b"`
	TickCurrent   *int32  `json:"tick_current,omitempty"`
	ActiveBinID   *int32  `json:"active_bin_id,omitempty"`
	BinStep       *uint16 `json:"bin_step,omitempty"`
	TotalValueUSD string  `json:"total_value_usd"`
}

// HolderResponse is one row.
type HolderResponse struct {
	Wallet          string `json:"wallet"`
	PositionAddress string `json:"position_address,omitempty"`
	Liquidity       string `json:"liquidity"`
	Percentage      string `json:"percentage"`
	TokenAmountA    string `json:"token_amount_a"`
	TokenAmountB    string `json:"token_amount_b"`
	ValueUSD        string `json:"value_usd"`
	PositionCount   int    `json:"position_count,omitempty"`
	TickLower       *int32 `json:"tick_lower,omitempty"`
	TickUpper       *int32 `json:"tick_upper,omitempty"`
	BinIDLower      *int32 `json:"bin_id_lower,omitempty"`
	BinIDUpper      *int32 `json:"bin_id_upper,omitempty"`
}

// NewPageResponse converts a service page to the API shape.
func NewPageResponse(p *service.Page) PageResponse {
	snap := p.Snapshot
	rows := make([]HolderResponse, 0, len(p.Holders))
	for _, h := range p.Holders {
		rows = append(rows, newHolderResponse(h))
	}
	return PageResponse{
		Pool:      newPoolResponse(snap),
		Holders:   rows,
		Total:     p.Total,
		Offset:    p.Offset,
		Limit:     p.Limit,
		GroupBy:   string(p.GroupBy),
		UpdatedAt: snap.FetchedAt.Unix(),
		Warnings:  snap.Warnings,
	}
}

func newPoolResponse(s *domain.Snapshot) PoolResponse {
	p := s.Pool
	out := PoolResponse{
		Address:       p.Address,
		Protocol:      string(p.Protocol),
		Kind:          string(p.Kind),
		TokenMintA:    p.MintA,
		TokenMintB:    p.MintB,
		TokenVaultA:   p.VaultA,
		TokenVaultB:   p.VaultB,
		LPMint:        p.LPMint,
		DecimalsA:     p.DecimalsA,
		DecimalsB:     p.DecimalsB,
		ReserveA:      FormatDecimal(poolmath.ToUI(p.ReserveA, p.DecimalsA)),
		ReserveB:      FormatDecimal(poolmath.ToUI(p.ReserveB, p.DecimalsB)),
		PriceAInB:     FormatDecimal(p.PriceAInB),
		TickCurrent:   p.TickCurrent,
		ActiveBinID:   p.ActiveBinID,
		BinStep:       p.BinStep,
		TotalValueUSD: FormatDecimal(s.TotalUSD),
	}
	if p.LPSupply != nil {
		out.LPSupply = p.LPSupply.String()
	}
	if p.LPMintSupply != nil {
		out.LPMintSupply = p.LPMintSupply.String()
	}
	return out
}

func newHolderResponse(h domain.Holder) HolderResponse {
	return HolderResponse{
		Wallet:          h.Wallet,
		PositionAddress: h.PositionAddress,
		Liquidity:       bigString(h.Liquidity),
		Percentage:      FormatDecimal(h.Percentage),
		TokenAmountA:    FormatDecimal(h.AmountA),
		TokenAmountB:    FormatDecimal(h.AmountB),
		ValueUSD:        FormatDecimal(h.ValueUSD),
		PositionCount:   h.PositionCount,
		TickLower:       h.TickLower,
		TickUpper:       h.TickUpper,
		BinIDLower:      h.BinIDLower,
		BinIDUpper:      h.BinIDUpper,
	}
}

// FormatDecimal renders a float without exponent notation.
func FormatDecimal(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func bigString(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}
