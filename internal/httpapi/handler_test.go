package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/service"
)

type fakeService struct {
	got  service.Query
	page *service.Page
	err  error
}

func (f *fakeService) Holders(_ context.Context, q service.Query) (*service.Page, error) {
	f.got = q
	return f.page, f.err
}

func samplePage() *service.Page {
	lower := int32(-10)
	return &service.Page{
		Snapshot: &domain.Snapshot{
			Pool: &domain.Pool{
				Address: "pool", Protocol: domain.ProtocolRaydiumCLMM, Kind: domain.KindPosition,
				ReserveA: big.NewInt(1_500_000_000), ReserveB: big.NewInt(0), DecimalsA: 9, PriceAInB: 1e-7,
			},
			FetchedAt: time.Unix(1_700_000_000, 0),
			Warnings:  []string{"w"},
		},
		Holders: []domain.Holder{{
			Wallet: "wallet", PositionAddress: "pos", Liquidity: new(big.Int).Lsh(big.NewInt(1), 100),
			Percentage: 12.5, AmountA: 0.000001, ValueUSD: 3, TickLower: &lower,
		}},
		Total: 1, Limit: 50, GroupBy: service.GroupByPosition,
	}
}

func do(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, Envelope) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return rec, env
}

func TestPostSuccess(t *testing.T) {
	svc := &fakeService{page: samplePage()}
	rec, env := do(t, NewHandler(svc).Routes(), http.MethodPost, "/v1/holder/liquidity-provider",
		`{"pool_address":" pool ","offset":5,"limit":20,"group_by":"Wallet","chain":"solana"}`)
	if rec.Code != http.StatusOK || !env.Success {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if svc.got.Pool != "pool" || svc.got.Offset != 5 || svc.got.Limit != 20 || svc.got.GroupBy != service.GroupByWallet {
		t.Fatalf("query = %+v", svc.got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"liquidity":"1267650600228229401496703205376"`, // u128 kept exact as string
		`"token_amount_a":"0.000001"`,                   // no exponent notation
		`"reserve_a":"1.5"`,
		`"price_a_in_b":"0.0000001"`,
		`"tick_lower":-10`,
		`"updated_at":1700000000`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}
}

func TestGetSuccess(t *testing.T) {
	svc := &fakeService{page: samplePage()}
	rec, _ := do(t, NewHandler(svc).Routes(), http.MethodGet, "/v1/holder/liquidity-provider?address=pool&limit=3&offset=1", "")
	if rec.Code != http.StatusOK || svc.got.Limit != 3 || svc.got.Offset != 1 {
		t.Fatalf("status %d query %+v", rec.Code, svc.got)
	}
	rec, _ = do(t, NewHandler(svc).Routes(), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status %d", rec.Code)
	}
}

func TestBadRequests(t *testing.T) {
	h := NewHandler(&fakeService{page: samplePage()}).Routes()
	cases := []struct{ method, target, body, code string }{
		{http.MethodPost, "/v1/holder/liquidity-provider", `{`, "invalid_body"},
		{http.MethodPost, "/v1/holder/liquidity-provider", `{"address":"p"}{"address":"q"}`, "invalid_body"},
		{http.MethodPost, "/v1/holder/liquidity-provider", `{}`, "address_required"},
		{http.MethodPost, "/v1/holder/liquidity-provider", `{"address":"p","group_by":"x"}`, "invalid_group_by"},
		{http.MethodPost, "/v1/holder/liquidity-provider", `{"address":"p","limit":-1}`, "invalid_pagination"},
		{http.MethodGet, "/v1/holder/liquidity-provider?address=p&limit=x", "", "invalid_limit"},
		{http.MethodGet, "/v1/holder/liquidity-provider?address=p&offset=x", "", "invalid_offset"},
	}
	for _, tc := range cases {
		rec, env := do(t, h, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusBadRequest || env.Success || env.Code != tc.code {
			t.Errorf("%s %s %s: status %d code %q", tc.method, tc.target, tc.body, rec.Code, env.Code)
		}
	}
}

func TestServiceErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidAddress, http.StatusBadRequest, "invalid_address"},
		{domain.ErrPoolNotFound, http.StatusNotFound, "pool_not_found"},
		{fmt.Errorf("x: %w", domain.ErrUnsupportedPool), http.StatusUnprocessableEntity, "unsupported_pool"},
		{domain.ErrPoolTooLarge, http.StatusUnprocessableEntity, "pool_too_large"},
		{domain.ErrBusy, http.StatusServiceUnavailable, "busy"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout"},
		{context.Canceled, 499, "client_closed_request"},
		{errors.New("rpc exploded with secret url"), http.StatusBadGateway, "upstream_error"},
	}
	for _, tc := range cases {
		rec, env := do(t, NewHandler(&fakeService{err: tc.err}).Routes(), http.MethodPost,
			"/v1/holder/liquidity-provider", `{"address":"p"}`)
		if rec.Code != tc.status || env.Code != tc.code {
			t.Errorf("%v: status %d code %q", tc.err, rec.Code, env.Code)
		}
		if strings.Contains(env.Message, "secret") {
			t.Errorf("internal error details leaked: %q", env.Message)
		}
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := map[float64]string{0: "0", 1e-9: "0.000000001", 12345678.5: "12345678.5"}
	for in, want := range cases {
		if got := FormatDecimal(in); got != want {
			t.Errorf("FormatDecimal(%v) = %s, want %s", in, got, want)
		}
	}
}
