package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/features/skills"
)

// Origin is where a skill installed from GitHub came from.
type Origin struct {
	Source    string    `json:"source"` // the link it was installed from
	Path      string    `json:"path"`   // its folder in that repository
	Hash      string    `json:"hash"`   // of its files as installed
	Installed time.Time `json:"installed"`
}

func isGitHub(source string) bool {
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	_, ok := parseGitHub(u)
	return ok
}

// skillHash is a hash of a skill's files (the origin file aside).
func skillHash(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for n := range files {
		if n != skills.OriginFile {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(files[n])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func originJSON(source, dir string, files map[string][]byte) []byte {
	b, _ := json.MarshalIndent(Origin{Source: source, Path: dir, Hash: skillHash(files), Installed: time.Now().UTC()}, "", "  ")
	return b
}

// SkillUpdate is an installed skill whose source has changed.
type SkillUpdate struct {
	Name   string `json:"name"`   // its folder name
	Title  string `json:"title"`  // its name from SKILL.md
	Agent  string `json:"agent"`  // "" = every agent's
	Source string `json:"source"` // the repository link
	Desc   string `json:"desc,omitempty"`
}

type originOf struct {
	sk     skills.Skill
	folder string
	o      Origin
}

func (s *Service) skillOrigins() []originOf {
	ss := s.skills()
	if ss == nil {
		return nil
	}
	var out []originOf
	for _, sk := range ss.All() {
		b, err := os.ReadFile(filepath.Join(sk.Dir, skills.OriginFile))
		if err != nil {
			continue
		}
		var o Origin
		if json.Unmarshal(b, &o) != nil || o.Source == "" {
			continue
		}
		out = append(out, originOf{sk: sk, folder: filepath.Base(sk.Dir), o: o})
	}
	return out
}

// fetchSkills downloads each source once and returns path → files.
func fetchSkills(ctx context.Context, source string, paths []string) (map[string]map[string][]byte, error) {
	u, err := url.Parse(source)
	if err != nil {
		return nil, err
	}
	t, ok := parseGitHub(u)
	if !ok {
		return nil, errors.New("not a GitHub link")
	}
	all, err := fetchArchive(ctx, t.archive(), t.dir, false, paths...)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string][]byte{}
	for _, dir := range paths {
		files := map[string][]byte{}
		for name, b := range all {
			rel := name
			if dir != "" {
				r, ok := strings.CutPrefix(name, dir+"/")
				if !ok {
					continue
				}
				rel = r
			}
			files[rel] = b
		}
		if _, ok := files["SKILL.md"]; ok {
			out[dir] = files
		}
	}
	return out, nil
}

// SkillUpdates checks the skills installed from GitHub against their
// repositories (one download per repository).
func (s *Service) SkillUpdates(ctx context.Context) ([]SkillUpdate, error) {
	by := map[string][]originOf{}
	for _, x := range s.skillOrigins() {
		by[x.o.Source] = append(by[x.o.Source], x)
	}
	out := []SkillUpdate{}
	var errs []string
	for src, xs := range by {
		paths := make([]string, 0, len(xs))
		for _, x := range xs {
			paths = append(paths, x.o.Path)
		}
		got, err := fetchSkills(ctx, src, paths)
		if err != nil {
			errs = append(errs, src+": "+err.Error())
			continue
		}
		for _, x := range xs {
			files, ok := got[x.o.Path]
			if !ok || skillHash(files) == x.o.Hash {
				continue // gone from the repository, or unchanged
			}
			out = append(out, SkillUpdate{Name: x.folder, Title: x.sk.Name, Agent: x.sk.Agent, Source: src, Desc: x.sk.Description})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	if len(out) == 0 && len(errs) > 0 {
		return out, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

// UpdateSkill installs the current version of a skill from its repository.
func (s *Service) UpdateSkill(ctx context.Context, agentID, folder string) (skills.Skill, error) {
	ss := s.skills()
	if ss == nil {
		return skills.Skill{}, errors.New("the skills service is not running")
	}
	for _, x := range s.skillOrigins() {
		if x.folder != folder || x.sk.Agent != agentID {
			continue
		}
		got, err := fetchSkills(ctx, x.o.Source, []string{x.o.Path})
		if err != nil {
			return skills.Skill{}, err
		}
		files, ok := got[x.o.Path]
		if !ok {
			return skills.Skill{}, fmt.Errorf("%s is no longer in %s", x.o.Path, x.o.Source)
		}
		files[skills.OriginFile] = originJSON(x.o.Source, x.o.Path, files)
		return ss.Replace(agentID, folder, files)
	}
	return skills.Skill{}, fmt.Errorf("%w: %s was not installed from GitHub", ErrInput, folder)
}
