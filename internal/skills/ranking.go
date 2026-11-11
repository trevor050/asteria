package skills

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

type UsageStats struct {
	Count    int       `json:"count"`
	LastUsed time.Time `json:"lastUsed"`
}

// Match is one search result. Value carries an inline parameter typed after
// the skill name ("resize 50%" -> Value "50%").
type Match struct {
	Skill Skill   `json:"skill"`
	Value string  `json:"value"`
	Score float64 `json:"score"`
	// Alias is the alias the query matched when the name itself did not,
	// so the UI can say why a row is there.
	Alias string `json:"alias,omitempty"`
}

// Context describes the files a search is for.
type Context struct {
	// InputTypes are the types of the files skills would apply to.
	InputTypes []string
	Usage      map[string]UsageStats
}

// Ranker scores skills against a query. Weights are tuned so a strong text
// match always beats frecency, and frecency breaks ties between equal matches.
type Ranker struct {
	HalfLifeDays    float64
	FrecencyWeight  float64
	FrecencyCap     float64
	InputMatchBoost float64
	NoOpPenalty     float64
	CategoryOrder   map[string]float64
}

func DefaultRanker() *Ranker {
	return &Ranker{
		HalfLifeDays:    14,
		FrecencyWeight:  60,
		FrecencyCap:     240,
		InputMatchBoost: 150,
		NoOpPenalty:     500,
		CategoryOrder: map[string]float64{
			"convert": 40, "transform": 35, "compress": 30, "adjust": 29, "pdf": 28, "media": 26,
			"archive": 24, "data": 22, "filter": 20, "compose": 18, "info": 16, "sizes": 14, "meta": 0,
		},
	}
}

const (
	strongMatch = 600
	weakMatch   = 440
)

var (
	compactValue = regexp.MustCompile(`^([a-z]+)(\d[\d.%x:,\-]*)$`)
	digit        = regexp.MustCompile(`\d`)
)

func valueLike(tok string) bool {
	return digit.MatchString(tok)
}

// SplitInline separates a trailing parameter value from the skill words:
// "resize 50%" -> ("resize", "50%"), "extract pages 1-3, 5" ->
// ("extract pages", "1-3, 5"). It returns value "" when there is none.
func SplitInline(q string) (string, string) {
	toks := strings.Fields(q)
	k := len(toks)
	for k > 1 && valueLike(toks[k-1]) {
		k--
	}
	if k == len(toks) || k == 0 {
		return strings.TrimSpace(q), ""
	}
	return strings.Join(toks[:k], " "), strings.Join(toks[k:], " ")
}

// Rank filters and orders skills for a query.
func (r *Ranker) Rank(all []Skill, query string, ctx Context) []Match {
	query = strings.TrimSpace(query)
	if query == "" {
		return r.rankEmpty(all, ctx)
	}
	text, value := SplitInline(query)
	matches := r.rankText(all, text, value, ctx)
	if len(matches) == 0 && value == "" {
		// "rs50" or "jpg80": letters then a value with no space.
		if m := compactValue.FindStringSubmatch(strings.ToLower(query)); m != nil {
			matches = r.rankText(all, m[1], m[2], ctx)
		}
	}
	if value == "" && strings.Contains(query, " ") && bestRaw(all, query) < strongMatch {
		// Word values: "dither gameboy", "watermark Draft", "replace color
		// red blue". When the whole query names nothing, take the longest
		// leading words that clearly name a skill with a parameter and use
		// the rest as its value.
		toks := strings.Fields(query)
		for k := len(toks) - 1; k >= 1; k-- {
			head := strings.Join(toks[:k], " ")
			best := 0.0
			for _, s := range all {
				if len(s.Params) > 0 {
					best = math.Max(best, matchScore(s, head))
				}
			}
			if best >= strongMatch {
				return r.rankText(all, head, strings.Join(toks[k:], " "), ctx)
			}
		}
	}
	return matches
}

func bestRaw(all []Skill, q string) float64 {
	best := 0.0
	for _, s := range all {
		best = math.Max(best, matchScore(s, q))
	}
	return best
}

func (r *Ranker) rankEmpty(all []Skill, ctx Context) []Match {
	out := []Match{}
	for _, s := range all {
		if !s.IsMeta && len(ctx.InputTypes) > 0 && !acceptsAny(s, ctx.InputTypes) {
			continue
		}
		score := r.CategoryOrder[s.Category] + clampPriority(s.Priority) + r.frecency(s.ID, ctx.Usage)
		if s.IsMeta {
			score -= 200
		}
		if r.isNoOp(s, ctx) {
			score -= r.NoOpPenalty
		}
		out = append(out, Match{Skill: s, Score: score})
	}
	sortMatches(out)
	return out
}

func (r *Ranker) rankText(all []Skill, text, value string, ctx Context) []Match {
	out := []Match{}
	raw := make([]float64, len(all))
	strongest := 0.0
	for i, s := range all {
		raw[i] = matchScore(s, text)
		if raw[i] > strongest {
			strongest = raw[i]
		}
	}
	for i, s := range all {
		ms := raw[i]
		// Fuzzy-only matches are noise when the query clearly names something.
		if ms <= 0 || (strongest >= strongMatch && ms < weakMatch) {
			continue
		}
		// Legible order: skills that apply to the files first; then how the
		// query matched (name exact, name prefix, name words, alias exact,
		// alias prefix, alias words, fuzzy); then, within a tier, everyday
		// skills (priority, the user's habits, family) ahead of obscure ones.
		tier, alias := matchTier(s, text)
		// App commands (export, undo, settings) compete with file skills only
		// when their name starts with what was typed.
		usable := len(ctx.InputTypes) == 0 || acceptsAny(s, ctx.InputTypes)
		if s.IsMeta {
			usable = tier >= tierNamePrefix
		}
		if r.isNoOp(s, ctx) {
			usable = false
		}
		within := clampPriority(s.Priority)*3 + r.CategoryOrder[s.Category]/4 + r.frecency(s.ID, ctx.Usage)
		score := float64(tier)*1e3 + within
		if usable {
			score += 1e5
		}
		m := Match{Skill: s, Score: score, Alias: alias}
		if value != "" && len(s.Params) > 0 {
			m.Value = value
		}
		out = append(out, m)
	}
	sortMatches(out)
	return out
}

func clampPriority(p float64) float64 { return math.Max(0, math.Min(20, p)) }

// isNoOp is true for a "Convert to X" skill when every file already is X,
// e.g. "Convert to PNG" on a batch of PNGs.
func (r *Ranker) isNoOp(s Skill, ctx Context) bool {
	if s.IsMeta || s.OutputType == "" || len(ctx.InputTypes) == 0 {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(s.Name), "convert to") {
		return false
	}
	for _, t := range ctx.InputTypes {
		if normExt(t) != normExt(s.OutputType) {
			return false
		}
	}
	return true
}

func normExt(e string) string {
	switch strings.ToLower(e) {
	case ".jpeg":
		return ".jpg"
	case ".tif":
		return ".tiff"
	case ".yml":
		return ".yaml"
	}
	return strings.ToLower(e)
}

func acceptsAny(s Skill, types []string) bool {
	for _, t := range types {
		if s.Accepts(t) {
			return true
		}
	}
	return false
}

func sortMatches(m []Match) {
	sort.SliceStable(m, func(i, j int) bool {
		if m[i].Score == m[j].Score {
			return m[i].Skill.Name < m[j].Skill.Name
		}
		return m[i].Score > m[j].Score
	})
}

func (r *Ranker) frecency(id string, usage map[string]UsageStats) float64 {
	stat, ok := usage[id]
	if !ok || stat.Count == 0 {
		return 0
	}
	age := time.Since(stat.LastUsed).Hours() / 24
	if age < 0 {
		age = 0
	}
	decay := math.Exp(-math.Ln2 / r.HalfLifeDays * age)
	return math.Min(r.FrecencyCap, float64(stat.Count)*r.FrecencyWeight*decay)
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Match tiers, best first.
const (
	tierNone = iota
	tierDescription
	tierTypo
	tierFuzzy
	tierAliasWords
	tierAliasPrefix
	tierAliasExact
	tierNameWords
	tierNamePrefix
	tierNameExact
)

// matchTier classifies how text matches a skill and returns the alias that
// matched when the match came from an alias.
func matchTier(s Skill, text string) (int, string) {
	q := strings.ToLower(strings.TrimSpace(text))
	if q == "" {
		return tierNone, ""
	}
	name := strings.ToLower(s.Name)
	qw := words(q)
	switch {
	case name == q:
		return tierNameExact, ""
	case strings.HasPrefix(name, q):
		return tierNamePrefix, ""
	case wordPrefixes(qw, words(s.Name)):
		return tierNameWords, ""
	}
	best, which := tierNone, ""
	for _, a := range s.Aliases {
		al := strings.ToLower(a)
		t := tierNone
		switch {
		case al == q:
			t = tierAliasExact
		case strings.HasPrefix(al, q):
			t = tierAliasPrefix
		case wordPrefixes(qw, words(a)):
			t = tierAliasWords
		case strings.Contains(al, q):
			t = tierFuzzy
		}
		if t > best {
			best, which = t, a
		}
	}
	if best >= tierAliasWords {
		return best, which
	}
	if strings.Contains(name, q) || subsequenceScore(name, strings.ReplaceAll(q, " ", "")) > 0 {
		return tierFuzzy, ""
	}
	if best == tierFuzzy {
		return tierFuzzy, which
	}
	if typoMatch(qw, words(s.Name)) {
		return tierTypo, ""
	}
	if len(q) >= 4 && strings.Contains(strings.ToLower(s.Description), q) {
		return tierDescription, ""
	}
	return tierNone, ""
}

// matchScore returns how well text matches a skill (0 = no match).
func matchScore(s Skill, text string) float64 {
	q := strings.ToLower(strings.TrimSpace(text))
	if q == "" {
		return 0
	}
	name := strings.ToLower(s.Name)
	best := 0.0
	consider := func(v float64) {
		if v > best {
			best = v
		}
	}
	if name == q {
		consider(1000)
	}
	for _, a := range s.Aliases {
		al := strings.ToLower(a)
		switch {
		case al == q:
			consider(950)
		case strings.HasPrefix(al, q):
			consider(760)
		case strings.Contains(al, q):
			consider(430)
		}
		if wordPrefixes(words(q), words(a)) {
			consider(560)
		}
	}
	switch {
	case strings.HasPrefix(name, q):
		consider(800)
	case strings.Contains(name, q):
		consider(480)
	}
	qw := words(q)
	if wordPrefixes(qw, words(s.Name)) {
		consider(620)
	}
	if v := subsequenceScore(name, strings.ReplaceAll(q, " ", "")); v > 0 {
		consider(v)
	}
	if typoMatch(qw, words(s.Name)) {
		consider(260)
	}
	if len(q) >= 4 && strings.Contains(strings.ToLower(s.Description), q) {
		consider(150)
	}
	return best
}

// wordPrefixes: every query word is a prefix of some target word.
func wordPrefixes(q, target []string) bool {
	if len(q) == 0 {
		return false
	}
	for _, w := range q {
		found := false
		for _, t := range target {
			if strings.HasPrefix(t, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// subsequenceScore rewards queries whose letters appear in order in the
// name, favouring word starts ("cvjpg" -> "Convert to JPEG").
func subsequenceScore(name, q string) float64 {
	if len(q) < 2 {
		return 0
	}
	nr, qr := []rune(name), []rune(q)
	qi, starts, gaps := 0, 0, 0
	last := -1
	for i := 0; i < len(nr) && qi < len(qr); i++ {
		if nr[i] != qr[qi] {
			continue
		}
		if i == 0 || !unicode.IsLetter(nr[i-1]) {
			starts++
		}
		if last >= 0 && i != last+1 {
			gaps++
		}
		last = i
		qi++
	}
	if qi < len(qr) {
		return 0
	}
	if starts == 0 {
		return 0
	}
	return math.Max(0, 340+float64(starts)*25-float64(gaps)*15)
}

// typoMatch allows one edit per query word of four or more letters.
func typoMatch(q, target []string) bool {
	if len(q) == 0 {
		return false
	}
	for _, w := range q {
		ok := false
		for _, t := range target {
			if strings.HasPrefix(t, w) || (len(w) >= 4 && levenshtein(w, t) <= 1) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
