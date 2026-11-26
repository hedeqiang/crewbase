package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekBalance(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"96.45","granted_balance":"0.00","topped_up_balance":"96.45"}]}`))
	}))
	defer srv.Close()
	balancePaths["127.0.0.1"] = "/user/balance"
	defer delete(balancePaths, "127.0.0.1")

	// a /v1 base URL still reads the balance at the host's root
	a, err := New(nil, ProviderConfig{ID: "deepseek", BaseURL: srv.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	q := a.Quota(context.Background())
	if auth != "Bearer k" {
		t.Fatalf("auth = %q", auth)
	}
	if len(q.Accounts) != 1 || q.Accounts[0].Balance != "¥96.45" || q.Accounts[0].Vendor != "deepseek" {
		t.Fatalf("quota = %+v", q)
	}
	if q.Known {
		t.Fatal("a balance is not a window: Known stays false (routing reads windows)")
	}
}

func TestNoBalanceElsewhere(t *testing.T) {
	a, err := New(nil, ProviderConfig{ID: "openai", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if q := a.Quota(context.Background()); q.Known || len(q.Accounts) > 0 {
		t.Fatalf("openai: %+v", q)
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[[2]string]string{{"CNY", "96.45"}: "¥96.45", {"USD", "3.2"}: "$3.20", {"EUR", "1"}: "1.00 EUR"} {
		if got := money(in[0], in[1]); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}
