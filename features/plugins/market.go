package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/bridge/process"
)

// MarketEntry is one plugin in the marketplace index (a JSON list).
type MarketEntry struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Version     string              `json:"version"`
	Source      string              `json:"source"` // local path ("./…" is relative to the index), tarball URL or npm name
	Permissions process.Permissions `json:"permissions"`
	Author      string              `json:"author,omitempty"`
	Repo        string              `json:"repo,omitempty"`
	// Integrity pins the package: "sha256-<base64>".
	Integrity string `json:"integrity,omitempty"`
	// Signed is the publisher's signature over the package.
	Signed *Signed `json:"signed,omitempty"`
	// Installed is the installed version, if any (filled by the manager).
	Installed string `json:"installed,omitempty"`
}

// Market fetches the marketplace index.
func (m *Manager) Market(ctx context.Context) ([]MarketEntry, error) {
	raw, base, err := m.marketIndex(ctx)
	if err != nil {
		return nil, err
	}
	var ents []MarketEntry
	if err := json.Unmarshal(raw, &ents); err != nil {
		return nil, fmt.Errorf("market index: %w", err)
	}
	if m.cfg.MarketBase != "" {
		base = m.cfg.MarketBase
	}
	inst := map[string]string{}
	for _, p := range m.Installed() {
		inst[p.Name] = p.Version
	}
	for i := range ents {
		s := ents[i].Source
		if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
			ents[i].Source = filepath.Join(base, filepath.FromSlash(s))
		}
		ents[i].Installed = inst[ents[i].Name]
	}
	return ents, nil
}

func (m *Manager) marketIndex(ctx context.Context) ([]byte, string, error) {
	u := m.cfg.MarketURL
	cwd, _ := os.Getwd()
	switch {
	case u == "":
		return defaultMarket, cwd, nil
	case strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "https://"):
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("market index: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("market index: HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return b, cwd, err
	default:
		p := strings.TrimPrefix(u, "file://")
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, "", fmt.Errorf("market index: %w", err)
		}
		abs, _ := filepath.Abs(p)
		return b, filepath.Dir(abs), nil
	}
}
