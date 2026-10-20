package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rpcCall struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func newServer(t *testing.T, h func(w http.ResponseWriter, call rpcCall)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var params []json.RawMessage
		_ = json.Unmarshal(raw.Params, &params)
		h(w, rpcCall{Method: raw.Method, Params: params})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func result(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v})
}

func fastClient(url string) *Client {
	return New(Config{Endpoint: url, MaxRetries: 3, BackoffBase: time.Millisecond, BackoffMax: 2 * time.Millisecond})
}

func b64(s string) []string { return []string{base64.StdEncoding.EncodeToString([]byte(s)), "base64"} }

func TestRetriesOnRateLimitThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := newServer(t, func(w http.ResponseWriter, _ rpcCall) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		result(w, map[string]any{"value": map[string]any{"owner": "P", "data": b64("hi")}})
	})
	acct, err := fastClient(srv.URL).GetAccountInfo(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	if string(acct.Data) != "hi" || acct.Owner != "P" || calls.Load() != 3 {
		t.Fatalf("got %+v after %d calls", acct, calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := newServer(t, func(w http.ResponseWriter, _ rpcCall) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})
	if _, err := fastClient(srv.URL).GetAccountInfo(context.Background(), "A"); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("400 must not be retried, got %d calls", calls.Load())
	}
}

func TestRPCErrorAndNotFound(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, call rpcCall) {
		if call.Method == "getAssetBatch" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		}
		result(w, map[string]any{"value": nil})
	})
	c := fastClient(srv.URL)
	if _, err := c.GetAccountInfo(context.Background(), "A"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("want ErrAccountNotFound, got %v", err)
	}
	if _, err := c.GetAssetBatch(context.Background(), []string{"m"}); !IsMethodNotFound(err) {
		t.Fatalf("want method not found, got %v", err)
	}
}

func TestErrorsNeverContainAPIKey(t *testing.T) {
	c := New(Config{Endpoint: "http://127.0.0.1:1/?api-key=SECRET", MaxRetries: -1, Timeout: time.Second})
	_, err := c.GetAccountInfo(context.Background(), "A")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error must not leak the endpoint: %v", err)
	}
}

func TestGetMultipleAccountsBatchesAndKeepsOrder(t *testing.T) {
	var batches atomic.Int32
	srv := newServer(t, func(w http.ResponseWriter, call rpcCall) {
		batches.Add(1)
		var keys []string
		_ = json.Unmarshal(call.Params[0], &keys)
		values := make([]any, len(keys))
		for i, k := range keys {
			if k == "missing" {
				continue
			}
			values[i] = map[string]any{"owner": "O", "data": b64(k)}
		}
		result(w, map[string]any{"value": values})
	})
	keys := make([]string, 250)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	keys[120] = "missing"
	out, err := fastClient(srv.URL).GetMultipleAccounts(context.Background(), keys)
	if err != nil {
		t.Fatal(err)
	}
	if batches.Load() != 3 {
		t.Errorf("want 3 batches of <=100, got %d", batches.Load())
	}
	for i, a := range out {
		if i == 120 {
			if a != nil {
				t.Error("missing account must be nil")
			}
			continue
		}
		if a == nil || string(a.Data) != keys[i] || a.Pubkey != keys[i] {
			t.Fatalf("index %d out of order: %+v", i, a)
		}
	}
}

func TestGetProgramAccountsV2FollowsPaginationKey(t *testing.T) {
	var pages atomic.Int32
	srv := newServer(t, func(w http.ResponseWriter, call rpcCall) {
		var cfg map[string]any
		_ = json.Unmarshal(call.Params[1], &cfg)
		n := pages.Add(1)
		var next any = fmt.Sprintf("page-%d", n)
		if n == 3 {
			next = nil
		}
		if n > 1 && cfg["paginationKey"] != fmt.Sprintf("page-%d", n-1) {
			t.Errorf("page %d sent paginationKey %v", n, cfg["paginationKey"])
		}
		// A short page (1 account) must not stop pagination.
		result(w, map[string]any{
			"accounts":      []any{map[string]any{"pubkey": fmt.Sprintf("p%d", n), "account": map[string]any{"owner": "O", "data": b64("x")}}},
			"paginationKey": next,
		})
	})
	out, err := fastClient(srv.URL).GetProgramAccountsV2(context.Background(), ProgramAccountsQuery{
		Program: "P", Filters: []Filter{DataSize(165)}, DataSlice: &DataSlice{Offset: 32, Length: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || pages.Load() != 3 {
		t.Fatalf("got %d accounts over %d pages", len(out), pages.Load())
	}
}

func TestGetProgramAccountsAndLargestAccounts(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, call rpcCall) {
		switch call.Method {
		case "getProgramAccounts":
			var cfg map[string]any
			_ = json.Unmarshal(call.Params[1], &cfg)
			if cfg["dataSlice"] == nil || cfg["filters"] == nil {
				t.Errorf("filters/dataSlice not sent: %v", cfg)
			}
			result(w, []any{map[string]any{"pubkey": "a", "account": map[string]any{"owner": "O", "data": b64("z")}}})
		case "getTokenLargestAccounts":
			result(w, map[string]any{"value": []any{map[string]any{"address": "ta", "amount": "1"}}})
		}
	})
	c := fastClient(srv.URL)
	accts, err := c.GetProgramAccounts(context.Background(), ProgramAccountsQuery{
		Program: "P", Filters: []Filter{MemcmpPubkey(8, "x"), Memcmp(0, []byte{1})}, DataSlice: &DataSlice{Length: 1},
	})
	if err != nil || len(accts) != 1 || accts[0].Pubkey != "a" {
		t.Fatalf("gPA = %+v, %v", accts, err)
	}
	largest, err := c.GetTokenLargestAccounts(context.Background(), "m")
	if err != nil || len(largest) != 1 || largest[0].Address != "ta" {
		t.Fatalf("largest = %+v, %v", largest, err)
	}
}

func TestCallHonoursContextCancellation(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ rpcCall) { w.WriteHeader(http.StatusServiceUnavailable) })
	c := New(Config{Endpoint: srv.URL, MaxRetries: 5, BackoffBase: time.Second, BackoffMax: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.GetAccountInfo(ctx, "A"); err == nil {
		t.Fatal("expected error")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("backoff did not stop on context cancellation")
	}
}
