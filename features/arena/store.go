package arena

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Categories of an arena's task (the stats and the suggestion are per
// category). The UI names them: 写代码 / 写作 / 分析 / 翻译 / 问答 / 其他.
const (
	CatCode      = "code"
	CatWriting   = "writing"
	CatAnalysis  = "analysis"
	CatTranslate = "translate"
	CatQA        = "qa"
	CatOther     = "other"
)

// Categories in the order the stats list them.
var Categories = []string{CatCode, CatWriting, CatAnalysis, CatTranslate, CatQA, CatOther}

// rules are tried in order; the first that matches names the category.
// Translation first ("把这段代码注释翻译成英文" is a translation), then
// code (a fence or a programming word), writing, analysis, questions.
var rules = []struct {
	cat string
	re  *regexp.Regexp
}{
	{CatTranslate, regexp.MustCompile(`(?i)翻译|译成|译为|译作|翻成|英译|中译|\btranslat|\binto (english|chinese|japanese|french|german|spanish|korean)\b`)},
	{CatCode, regexp.MustCompile("(?i)```|代码|函数|编程|程序|脚本|报错|调试|重构|单元测试|正则|接口|算法|编译|前端|后端|数据库|\\bbug\\b|\\bsql\\b|\\bregex|\\bapi\\b|\\bcode\\b|\\bfunction\\b|\\bscript\\b|\\brefactor|\\bdebug|\\bcompile|\\bpython\\b|\\bgolang\\b|\\bjava(script)?\\b|\\btypescript\\b|\\brust\\b|\\breact\\b|\\bhtml\\b|\\bcss\\b|\\bshell\\b|\\bbash\\b|\\bc\\+\\+")},
	{CatWriting, regexp.MustCompile(`(?i)写一[篇首封段个]|文章|作文|文案|邮件|诗|故事|小说|剧本|演讲稿|稿子|润色|改写|扩写|缩写|标题|口号|周报|简历|祝福|\bwrite (an?|the|me)\b|\bessay\b|\bpoem\b|\bstory\b|\bemail\b|\bletter\b|\bblog\b|\bslogan\b|\brewrite\b|\bpolish\b`)},
	{CatAnalysis, regexp.MustCompile(`(?i)分析|对比|比较|评估|评价|优缺点|利弊|趋势|原因|总结|归纳|研判|测算|数据|报告|\banaly[sz]|\bcompare\b|\bcomparison\b|\bevaluat|\bpros and cons\b|\bsummari[sz]e\b|\btrade-?offs?\b`)},
	{CatQA, regexp.MustCompile(`(?i)[?？]\s*$|^(什么|为什么|怎么|怎样|如何|哪|谁|是否|能否|可以|请问)|什么|为什么|怎么|如何|吗[?？。]?\s*$|\b(what|why|how|when|where|who|which|is|are|can|does|do)\b`)},
}

// Categorize names the category of a task by simple rules.
func Categorize(task string) string {
	s := strings.TrimSpace(task)
	if s == "" {
		return CatOther
	}
	for _, r := range rules {
		if r.re.MatchString(s) {
			return r.cat
		}
	}
	return CatOther
}

// Record is one arena as the store keeps it (no answers: those stay in
// the conversation's sessions).
type Record struct {
	ID       string    `json:"id"`
	Session  string    `json:"session"`
	At       time.Time `json:"at"`
	Task     string    `json:"task"` // cut to taskMax
	Category string    `json:"category"`
	Models   []string  `json:"models"`
	Runs     []RunStat `json:"runs"`
	// Winner is the picked model ("" until one is picked).
	Winner string `json:"winner,omitempty"`
}

// RunStat is one model's run in an arena.
type RunStat struct {
	Model   string  `json:"model"`
	Status  string  `json:"status,omitempty"` // "" running | completed | failed | cancelled
	Ms      int64   `json:"ms,omitempty"`
	Input   int     `json:"input,omitempty"`
	Output  int     `json:"output,omitempty"`
	CostUSD float64 `json:"costUsd,omitempty"`
	Priced  bool    `json:"priced,omitempty"`
}

const (
	taskMax    = 300  // runes of the task kept
	maxRecords = 2000 // the oldest go first
)

// Store keeps arena records in <dir>/arena.json.
type Store struct {
	path string
	mu   sync.Mutex
	recs []Record
}

// OpenStore loads <dir>/arena.json (missing = empty; "" dir = in memory).
func OpenStore(dir string) (*Store, error) {
	s := &Store{}
	if dir == "" {
		return s, nil
	}
	s.path = filepath.Join(dir, "arena.json")
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Records []Record `json:"records"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	s.recs = f.Records
	return s, nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if len(s.recs) > maxRecords {
		s.recs = append([]Record(nil), s.recs[len(s.recs)-maxRecords:]...)
	}
	b, err := json.MarshalIndent(struct {
		Records []Record `json:"records"`
	}{s.recs}, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Add records a new arena.
func (s *Store) Add(r Record) error {
	if rs := []rune(r.Task); len(rs) > taskMax {
		r.Task = string(rs[:taskMax]) + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return s.saveLocked()
}

// update changes the record id (no-op when it is gone).
func (s *Store) update(id string, f func(*Record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.recs) - 1; i >= 0; i-- {
		if s.recs[i].ID == id {
			f(&s.recs[i])
			return s.saveLocked()
		}
	}
	return nil
}

// SetRun records how model i's run went.
func (s *Store) SetRun(id string, i int, run RunStat) error {
	return s.update(id, func(r *Record) {
		if i >= 0 && i < len(r.Runs) {
			r.Runs[i] = run
		}
	})
}

// SetWinner records the picked model.
func (s *Store) SetWinner(id, model string) error {
	return s.update(id, func(r *Record) { r.Winner = model })
}

// Records returns a copy of every record, oldest first.
func (s *Store) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.recs...)
}

// ModelStat is one model's record in one category.
type ModelStat struct {
	Model string `json:"model"`
	// Runs: arenas it answered in (completed); Wins: times it was picked.
	Runs int `json:"runs"`
	Wins int `json:"wins"`
	// AvgCostUSD is its mean cost per completed, priced run; AvgMs its
	// mean time per completed run.
	AvgCostUSD float64 `json:"avgCostUsd"`
	Priced     bool    `json:"priced"`
	AvgMs      int64   `json:"avgMs"`
}

// CategoryStat is one category's arenas.
type CategoryStat struct {
	Category string      `json:"category"`
	Arenas   int         `json:"arenas"`
	Picked   int         `json:"picked"`
	Models   []ModelStat `json:"models"` // most wins first, then cheapest
}

// Stats is the 擂台 report.
type Stats struct {
	Arenas     int            `json:"arenas"`
	Picked     int            `json:"picked"`
	Categories []CategoryStat `json:"categories"`
}

// Stats sums the records per category and model.
func (s *Store) Stats() Stats {
	type acc struct {
		ModelStat
		cost   float64
		priced int
		ms     int64
	}
	by := map[string]*CategoryStat{}
	models := map[string]map[string]*acc{}
	var st Stats
	for _, r := range s.Records() {
		cs := by[r.Category]
		if cs == nil {
			cs = &CategoryStat{Category: r.Category}
			by[r.Category] = cs
			models[r.Category] = map[string]*acc{}
		}
		cs.Arenas++
		st.Arenas++
		if r.Winner != "" {
			cs.Picked++
			st.Picked++
		}
		for _, run := range r.Runs {
			if run.Status != "completed" {
				continue
			}
			a := models[r.Category][run.Model]
			if a == nil {
				a = &acc{ModelStat: ModelStat{Model: run.Model}}
				models[r.Category][run.Model] = a
			}
			a.Runs++
			a.ms += run.Ms
			if run.Priced {
				a.priced++
				a.cost += run.CostUSD
			}
			if r.Winner == run.Model {
				a.Wins++
			}
		}
	}
	for _, cat := range Categories {
		cs := by[cat]
		if cs == nil {
			continue
		}
		for _, a := range models[cat] {
			m := a.ModelStat
			if a.Runs > 0 {
				m.AvgMs = a.ms / int64(a.Runs)
			}
			if a.priced > 0 {
				m.AvgCostUSD, m.Priced = a.cost/float64(a.priced), true
			}
			cs.Models = append(cs.Models, m)
		}
		sortModels(cs.Models)
		if cs.Models == nil {
			cs.Models = []ModelStat{}
		}
		st.Categories = append(st.Categories, *cs)
	}
	if st.Categories == nil {
		st.Categories = []CategoryStat{}
	}
	return st
}

// sortModels: most wins, then the best win rate, then the cheapest.
func sortModels(ms []ModelStat) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if a.Wins != b.Wins {
			return a.Wins > b.Wins
		}
		if ra, rb := float64(a.Wins)/float64(max(a.Runs, 1)), float64(b.Wins)/float64(max(b.Runs, 1)); ra != rb {
			return ra > rb
		}
		if a.Priced && b.Priced && a.AvgCostUSD != b.AvgCostUSD {
			return a.AvgCostUSD < b.AvgCostUSD
		}
		return a.Model < b.Model
	})
}

// Suggestion is the model this person usually picks for a kind of task.
type Suggestion struct {
	Category string `json:"category"`
	// Model is the one picked most for this category ("" when none was
	// picked yet); Wins of Picked arenas of this category.
	Model      string  `json:"model,omitempty"`
	Wins       int     `json:"wins,omitempty"`
	Picked     int     `json:"picked,omitempty"`
	AvgCostUSD float64 `json:"avgCostUsd,omitempty"`
	Priced     bool    `json:"priced,omitempty"`
	// Ranked are the category's models that have won, best first.
	Ranked []string `json:"ranked"`
}

// Suggest names the category of task and its usual winner.
func (s *Store) Suggest(task string) Suggestion {
	out := Suggestion{Category: Categorize(task), Ranked: []string{}}
	for _, c := range s.Stats().Categories {
		if c.Category != out.Category {
			continue
		}
		out.Picked = c.Picked
		for _, m := range c.Models {
			if m.Wins == 0 {
				continue
			}
			if out.Model == "" {
				out.Model, out.Wins, out.AvgCostUSD, out.Priced = m.Model, m.Wins, m.AvgCostUSD, m.Priced
			}
			out.Ranked = append(out.Ranked, m.Model)
		}
	}
	return out
}
