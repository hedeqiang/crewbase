package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/magpie"
)

// Per-model switches (Settings › Models). An off model is offered by no
// picker and never chosen automatically (llm.Registry.ModelOff). Where it
// is kept depends on the provider, so there is one concept per provider:
//
//   - magpie: its enabled list (magpie.json, Discovery.SetEnabled);
//   - providers added here: Provider.Models, the models the person chose
//     (providers.json); turning one off removes it from that list;
//   - everything else (subscription CLIs, local models, configured
//     providers): <dir>/model-switches.json, the off model ids per
//     provider, applied by llm.Registry.ModelsOf.

const switchesFile = "model-switches.json"

// switchStore is model-switches.json: provider id -> off model ids.
type switchStore struct {
	path string
	mu   sync.Mutex
	off  map[string][]string
}

func loadSwitches(path string) (*switchStore, error) {
	s := &switchStore{path: path, off: map[string][]string{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.off); err != nil {
		return nil, errors.New(switchesFile + ": " + err.Error())
	}
	if s.off == nil {
		s.off = map[string][]string{}
	}
	return s, nil
}

func (s *switchStore) isOff(provider, model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.off[provider], model)
}

func (s *switchStore) set(provider, model string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.off[provider]
	has := slices.Contains(cur, model)
	switch {
	case on && has:
		cur = slices.DeleteFunc(slices.Clone(cur), func(x string) bool { return x == model })
	case !on && !has:
		cur = append(slices.Clone(cur), model)
		sort.Strings(cur)
	default:
		return nil
	}
	next := make(map[string][]string, len(s.off))
	for k, v := range s.off {
		next[k] = v
	}
	if len(cur) == 0 {
		delete(next, provider)
	} else {
		next[provider] = cur
	}
	if s.path != "" {
		b, _ := json.MarshalIndent(next, "", "  ")
		tmp := s.path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, s.path); err != nil {
			return err
		}
	}
	s.off = next
	return nil
}

// Off implements llm.Switches for every kind of provider.
func (s *Service) Off(provider, model string) bool {
	if provider == magpie.ProviderID {
		if d, ok := kernel.Lookup[*magpie.Discovery](s.c, magpie.ServiceKey); ok {
			return !d.IsEnabled(model)
		}
		return false
	}
	if p, ok := s.Get(provider); ok {
		return len(p.Models) > 0 && !slices.Contains(p.Models, model)
	}
	return s.switches != nil && s.switches.isOff(provider, model)
}

// ModelSwitches lists every model of provider with its switch: Enabled
// is the switch. Magpie lists everything it serves; a provider added here
// lists its listing (fetched with the stored key) when the person chose
// some of it, so the others can be switched on again.
func (s *Service) ModelSwitches(ctx context.Context, provider string) ([]llm.ModelInfo, error) {
	if provider == magpie.ProviderID {
		d, ok := kernel.Lookup[*magpie.Discovery](s.c, magpie.ServiceKey)
		if !ok {
			return nil, errNotFound
		}
		ch := d.Choice()
		ms := s.llms.Enrich(provider, ch.Models)
		for i := range ms {
			ms[i].Enabled = ch.Enabled == nil || slices.Contains(ch.Enabled, ms[i].ID)
		}
		return ms, nil
	}
	ad, ok := s.llms.Get(provider)
	if !ok {
		return nil, errNotFound
	}
	mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	ms, err := s.llms.ModelsOf(mctx, ad)
	cancel()
	if err != nil {
		return nil, err
	}
	p, mine := s.Get(provider)
	if !mine || len(p.Models) == 0 {
		return ms, nil
	}
	for i := range ms {
		ms[i].Enabled = true
	}
	t := s.Test(ctx, Input{ID: p.ID, Name: p.Name, Preset: p.Preset, API: p.API, BaseURL: p.BaseURL})
	for _, m := range t.Models {
		if !slices.Contains(p.Models, m.ID) {
			m.Provider, m.Enabled = provider, false
			ms = append(ms, m)
		}
	}
	return ms, nil
}

// errLastModel: a provider added here needs one chosen model (none chosen
// means all of them).
var errLastModel = errors.New("at least one model of this provider must stay on; remove the provider instead")

// SetModelEnabled switches one model of provider on or off.
func (s *Service) SetModelEnabled(ctx context.Context, provider, model string, on bool) error {
	model = strings.TrimSpace(model)
	if provider == "" || model == "" || len(model) > 300 {
		return errors.New("provider and model are required")
	}
	if provider == magpie.ProviderID {
		d, ok := kernel.Lookup[*magpie.Discovery](s.c, magpie.ServiceKey)
		if !ok {
			return errNotFound
		}
		ch := d.Choice()
		all := make([]string, 0, len(ch.Models))
		for _, m := range ch.Models {
			all = append(all, m.ID)
		}
		cur := ch.Enabled
		if cur == nil {
			cur = all
		}
		next := slices.DeleteFunc(slices.Clone(cur), func(x string) bool { return x == model })
		if on {
			next = append(next, model)
		}
		if !slices.ContainsFunc(all, func(id string) bool { return !slices.Contains(next, id) }) {
			next = nil // everything magpie lists is on: new models will be too
		} else if next == nil {
			next = []string{}
		}
		return d.SetEnabled(next)
	}
	if p, ok := s.Get(provider); ok {
		cur := p.Models
		if len(cur) == 0 {
			if on {
				return nil // all are on
			}
			ad, ok := s.llms.Get(provider)
			if !ok {
				return errNotFound
			}
			mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			ms, err := ad.Models(mctx)
			cancel()
			if err != nil {
				return err
			}
			for _, m := range ms {
				cur = append(cur, m.ID)
			}
		}
		next := slices.DeleteFunc(slices.Clone(cur), func(x string) bool { return x == model })
		if on {
			next = append(next, model)
		}
		if len(next) == 0 {
			return errLastModel
		}
		if slices.Equal(next, p.Models) {
			return nil
		}
		_, err := s.Update(provider, Patch{Models: &next})
		return err
	}
	if _, ok := s.llms.Get(provider); !ok {
		return errNotFound
	}
	if err := s.switches.set(provider, model, on); err != nil {
		return err
	}
	llm.AdaptersChanged.Emit(s.c.Host(), struct{}{})
	return nil
}
