package rpc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"birdeye.so/lp-holder/internal/solana"
)

const (
	multipleAccountsBatch = 100  // Solana hard limit for getMultipleAccounts
	gpaV2PageLimit        = 5000 // Helius getProgramAccountsV2: 1..10000
	gpaV2MaxPages         = 1000 // guard against a server that never ends pagination
	commitment            = "confirmed"
)

// ErrAccountNotFound is returned when an account does not exist.
var ErrAccountNotFound = errors.New("account not found")

// Account is a decoded on-chain account (or a dataSlice of it).
type Account struct {
	Pubkey string
	Owner  string
	Data   []byte
}

// DataSlice limits the returned account bytes.
type DataSlice struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
}

// Filter is a getProgramAccounts filter (memcmp or dataSize).
type Filter map[string]any

// Memcmp matches raw bytes at offset.
func Memcmp(offset int, b []byte) Filter {
	return Filter{"memcmp": map[string]any{"offset": offset, "bytes": solana.EncodeBase58(b)}}
}

// MemcmpPubkey matches a base58 public key at offset.
func MemcmpPubkey(offset int, pubkey string) Filter {
	return Filter{"memcmp": map[string]any{"offset": offset, "bytes": pubkey}}
}

// DataSize matches the exact account size.
func DataSize(n int) Filter {
	return Filter{"dataSize": n}
}

type wireAccount struct {
	Owner string   `json:"owner"`
	Data  []string `json:"data"`
}

func (w *wireAccount) decode(pubkey string) (Account, error) {
	if len(w.Data) == 0 {
		return Account{}, fmt.Errorf("account %s: empty data", pubkey)
	}
	data, err := base64.StdEncoding.DecodeString(w.Data[0])
	if err != nil {
		return Account{}, fmt.Errorf("account %s: base64: %w", pubkey, err)
	}
	return Account{Pubkey: pubkey, Owner: w.Owner, Data: data}, nil
}

// GetAccountInfo fetches one account. Returns ErrAccountNotFound when missing.
func (c *Client) GetAccountInfo(ctx context.Context, address string) (*Account, error) {
	var res struct {
		Value *wireAccount `json:"value"`
	}
	params := []any{address, map[string]any{"encoding": "base64", "commitment": commitment}}
	if err := c.Call(ctx, "getAccountInfo", params, &res); err != nil {
		return nil, err
	}
	if res.Value == nil {
		return nil, ErrAccountNotFound
	}
	acct, err := res.Value.decode(address)
	if err != nil {
		return nil, err
	}
	return &acct, nil
}

// GetMultipleAccounts fetches accounts in batches of 100, in parallel, keeping
// input order. Missing accounts are returned as nil.
func (c *Client) GetMultipleAccounts(ctx context.Context, addresses []string) ([]*Account, error) {
	out := make([]*Account, len(addresses))
	g, gctx := errgroup.WithContext(ctx)
	for start := 0; start < len(addresses); start += multipleAccountsBatch {
		end := min(start+multipleAccountsBatch, len(addresses))
		g.Go(func() error {
			return c.getMultipleBatch(gctx, addresses[start:end], out[start:end])
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) getMultipleBatch(ctx context.Context, addresses []string, dst []*Account) error {
	var res struct {
		Value []*wireAccount `json:"value"`
	}
	params := []any{addresses, map[string]any{"encoding": "base64", "commitment": commitment}}
	if err := c.Call(ctx, "getMultipleAccounts", params, &res); err != nil {
		return err
	}
	if len(res.Value) != len(addresses) {
		return fmt.Errorf("getMultipleAccounts: got %d accounts for %d keys", len(res.Value), len(addresses))
	}
	for i, w := range res.Value {
		if w == nil {
			continue
		}
		acct, err := w.decode(addresses[i])
		if err != nil {
			return err
		}
		dst[i] = &acct
	}
	return nil
}

// ProgramAccountsQuery describes a getProgramAccounts scan.
type ProgramAccountsQuery struct {
	Program   string
	Filters   []Filter
	DataSlice *DataSlice
}

func (q ProgramAccountsQuery) config() map[string]any {
	cfg := map[string]any{"encoding": "base64", "commitment": commitment}
	if len(q.Filters) > 0 {
		cfg["filters"] = q.Filters
	}
	if q.DataSlice != nil {
		cfg["dataSlice"] = q.DataSlice
	}
	return cfg
}

type wireKeyedAccount struct {
	Pubkey  string      `json:"pubkey"`
	Account wireAccount `json:"account"`
}

func decodeKeyed(items []wireKeyedAccount, dst []Account) ([]Account, error) {
	for i := range items {
		acct, err := items[i].Account.decode(items[i].Pubkey)
		if err != nil {
			return nil, err
		}
		dst = append(dst, acct)
	}
	return dst, nil
}

// GetProgramAccounts runs a standard getProgramAccounts scan.
func (c *Client) GetProgramAccounts(ctx context.Context, q ProgramAccountsQuery) ([]Account, error) {
	var res []wireKeyedAccount
	if err := c.Call(ctx, "getProgramAccounts", []any{q.Program, q.config()}, &res); err != nil {
		return nil, err
	}
	return decodeKeyed(res, make([]Account, 0, len(res)))
}

// GetProgramAccountsV2 runs Helius' paginated getProgramAccountsV2 until
// paginationKey is null. A short page does not mean the end (Helius docs).
func (c *Client) GetProgramAccountsV2(ctx context.Context, q ProgramAccountsQuery) ([]Account, error) {
	var all []Account
	var key *string
	for page := 0; page < gpaV2MaxPages; page++ {
		cfg := q.config()
		cfg["limit"] = gpaV2PageLimit
		if key != nil {
			cfg["paginationKey"] = *key
		}
		var res struct {
			Accounts      []wireKeyedAccount `json:"accounts"`
			PaginationKey *string            `json:"paginationKey"`
		}
		if err := c.Call(ctx, "getProgramAccountsV2", []any{q.Program, cfg}, &res); err != nil {
			return nil, err
		}
		var err error
		if all, err = decodeKeyed(res.Accounts, all); err != nil {
			return nil, err
		}
		if res.PaginationKey == nil || *res.PaginationKey == "" {
			return all, nil
		}
		key = res.PaginationKey
	}
	return nil, fmt.Errorf("getProgramAccountsV2: exceeded %d pages", gpaV2MaxPages)
}

// TokenBalance is one entry of getTokenLargestAccounts.
type TokenBalance struct {
	Address string `json:"address"`
	Amount  string `json:"amount"`
}

// GetTokenLargestAccounts returns the largest token accounts of a mint.
// Works for SPL Token and Token-2022 mints.
func (c *Client) GetTokenLargestAccounts(ctx context.Context, mint string) ([]TokenBalance, error) {
	var res struct {
		Value []TokenBalance `json:"value"`
	}
	params := []any{mint, map[string]any{"commitment": commitment}}
	if err := c.Call(ctx, "getTokenLargestAccounts", params, &res); err != nil {
		return nil, err
	}
	return res.Value, nil
}

// GetAssetBatch calls the DAS getAssetBatch method (Helius) and returns
// asset id -> current owner. Callers chunk ids to the provider limit.
func (c *Client) GetAssetBatch(ctx context.Context, ids []string) (map[string]string, error) {
	var res []*struct {
		ID        string `json:"id"`
		Ownership struct {
			Owner string `json:"owner"`
		} `json:"ownership"`
	}
	if err := c.Call(ctx, "getAssetBatch", map[string]any{"ids": ids}, &res); err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(res))
	for _, a := range res {
		if a != nil && a.Ownership.Owner != "" {
			owners[a.ID] = a.Ownership.Owner
		}
	}
	return owners, nil
}
