package price

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPricesParsesAndSendsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/defi/multi_price" || r.URL.Query().Get("list_address") != "a,b" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.Header.Get("X-API-KEY") != "k" || r.Header.Get("x-chain") != "solana" {
			t.Errorf("missing headers: %v", r.Header)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"a":{"value":1.5},"b":null,"c":{"value":0}}}`))
	}))
	defer srv.Close()

	got, err := New(Config{BaseURL: srv.URL + "/", Path: "/defi/multi_price", APIKey: "k"}).Prices(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["a"] != 1.5 {
		t.Fatalf("prices = %v", got)
	}
}

func TestPricesErrors(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"http error":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"success=false": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":false}`)) },
		"bad json":      func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) },
	} {
		srv := httptest.NewServer(handler)
		if _, err := New(Config{BaseURL: srv.URL}).Prices(context.Background(), []string{"a"}); err == nil {
			t.Errorf("%s: expected error", name)
		}
		srv.Close()
	}
}
