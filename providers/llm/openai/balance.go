package openai

// A provider's balance, where its API tells it with the same key:
//
//   DeepSeek  GET https://api.deepseek.com/user/balance
//             {"is_available": true, "balance_infos": [{"currency": "CNY",
//              "total_balance": "96.45", "granted_balance": "0.00",
//              "topped_up_balance": "96.45"}]}
//
// Others (OpenAI's own API among them) give an ordinary key no balance:
// Quota stays empty and 设置 shows no 额度 for them.
//
// The read is cached (balanceTTL) and runs in the background: the model
// list waits for it at most balanceWait, else answers with the last one.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

const (
	balanceTTL     = 60 * time.Second
	balanceFailTTL = 30 * time.Second
	balanceWait    = 3 * time.Second
)

type balanceCache struct {
	mu       sync.Mutex
	q        llm.Quota
	have     bool
	read     time.Time
	retryAt  time.Time
	inflight chan struct{}
}

// balanceURL is where this provider tells its balance ("" when it does not).
func (a *Adapter) balanceURL() string {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	if path, ok := balancePaths[strings.ToLower(u.Hostname())]; ok {
		return u.Scheme + "://" + u.Host + path
	}
	return ""
}

// balancePaths: the hosts that tell a key's balance, and where.
var balancePaths = map[string]string{
	"api.deepseek.com": "/user/balance",
}

// Quota reports the provider's balance (empty when its API has none).
func (a *Adapter) Quota(ctx context.Context) llm.Quota {
	endpoint := a.balanceURL()
	if endpoint == "" {
		return llm.Quota{}
	}
	c := &a.balance
	c.mu.Lock()
	now := time.Now()
	if (c.have && now.Sub(c.read) < balanceTTL) || now.Before(c.retryAt) {
		q := c.q
		c.mu.Unlock()
		return q
	}
	if c.inflight == nil {
		done := make(chan struct{})
		c.inflight = done
		go func() {
			defer close(done)
			fctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			q, err := a.fetchBalance(fctx, endpoint)
			c.mu.Lock()
			defer c.mu.Unlock()
			c.inflight = nil
			if err != nil {
				c.retryAt = time.Now().Add(balanceFailTTL)
				return
			}
			c.q, c.have, c.read, c.retryAt = q, true, time.Now(), time.Time{}
		}()
	}
	done := c.inflight
	if c.have { // stale is still worth showing; the refresh lands next time
		q := c.q
		c.mu.Unlock()
		return q
	}
	c.mu.Unlock()
	t := time.NewTimer(balanceWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.q
}

type deepseekBalance struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

func (a *Adapter) fetchBalance(ctx context.Context, endpoint string) (llm.Quota, error) {
	key, err := a.keys.Key(ctx, a.id)
	if err != nil {
		return llm.Quota{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return llm.Quota{}, err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return llm.Quota{}, transportError(ctx, a.id, err, key)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return llm.Quota{}, httpError(a.id, resp, key)
	}
	var b deepseekBalance
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return llm.Quota{}, err
	}
	var parts []string
	for _, bi := range b.BalanceInfos {
		parts = append(parts, money(bi.Currency, bi.TotalBalance))
	}
	if len(parts) == 0 {
		return llm.Quota{}, nil
	}
	return llm.Quota{Accounts: []llm.QuotaAccount{{
		Vendor: "deepseek", VendorName: "DeepSeek", Balance: strings.Join(parts, " · "),
	}}}, nil
}

// money writes an amount with its currency's sign (¥96.45, $3.20).
func money(currency, amount string) string {
	if f, err := strconv.ParseFloat(amount, 64); err == nil {
		amount = strconv.FormatFloat(f, 'f', 2, 64)
	}
	switch strings.ToUpper(currency) {
	case "CNY", "RMB":
		return "¥" + amount
	case "USD":
		return "$" + amount
	case "":
		return amount
	}
	return amount + " " + currency
}

var _ llm.QuotaReporter = (*Adapter)(nil)
