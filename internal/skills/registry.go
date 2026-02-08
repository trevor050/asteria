package skills

import (
	"context"
	"io/fs"
	"strings"
)

type RegistryOptions struct {
	EmbeddedFS    fs.FS
	EmbeddedRoot  string
	DiskCoreRoot  string
	CommunityRoot string
}

type Registry struct {
	loader *Loader
	ranker *Ranker
}

func NewRegistry(opts RegistryOptions) *Registry {
	loader := NewLoader(LoaderOptions{
		EmbeddedFS:    opts.EmbeddedFS,
		EmbeddedRoot:  opts.EmbeddedRoot,
		DiskCoreRoot:  opts.DiskCoreRoot,
		CommunityRoot: opts.CommunityRoot,
	})
	_ = loader.LoadAll()
	return &Registry{loader: loader, ranker: DefaultRanker()}
}

func (r *Registry) List() []Skill {
	if r.loader == nil {
		return nil
	}
	return r.loader.List()
}

func (r *Registry) StartHotReload(ctx context.Context) error {
	if r.loader == nil {
		return nil
	}
	return r.loader.Watch(ctx)
}

func (r *Registry) Changes() <-chan struct{} {
	if r.loader == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return r.loader.Changes()
}

func (r *Registry) Search(query string, inputTypes []string, usage map[string]UsageStats) []Skill {
	candidates := r.List()
	trimmed := strings.TrimSpace(query)

	// If no query, return all skills ranked by frecency and input match
	if trimmed == "" {
		filtered := make([]Skill, 0, len(candidates))
		for _, skill := range candidates {
			if skill.IsMeta || inputMatches(skill, inputTypes) || len(inputTypes) == 0 {
				filtered = append(filtered, skill)
			}
		}
		return r.ranker.Rank(filtered, "", inputTypes, usage)
	}

	// With a query, filter to only matching skills
	filtered := make([]Skill, 0, len(candidates))
	q := strings.ToLower(trimmed)

	for _, skill := range candidates {
		// Check if skill matches the query
		if matchesQuery(skill, q) {
			// Also check input type compatibility (or meta skills)
			if skill.IsMeta || inputMatches(skill, inputTypes) || len(inputTypes) == 0 {
				filtered = append(filtered, skill)
			}
		}
	}

	return r.ranker.Rank(filtered, trimmed, inputTypes, usage)
}

// matchesQuery checks if a skill matches the search query
func matchesQuery(skill Skill, query string) bool {
	// Check name
	name := strings.ToLower(skill.Name)
	if strings.Contains(name, query) || strings.HasPrefix(name, query) {
		return true
	}

	// Check aliases
	for _, alias := range skill.Aliases {
		a := strings.ToLower(alias)
		if strings.Contains(a, query) || strings.HasPrefix(a, query) {
			return true
		}
	}

	// Check description
	desc := strings.ToLower(skill.Description)
	if strings.Contains(desc, query) {
		return true
	}

	// Check individual words in query against name words
	queryWords := strings.Fields(query)
	nameWords := strings.Fields(name)
	for _, qw := range queryWords {
		for _, nw := range nameWords {
			if strings.HasPrefix(nw, qw) {
				return true
			}
		}
	}

	// Fuzzy: allow typos if query is close enough
	if len(query) >= 3 {
		dist := levenshteinDistance(name, query)
		if dist <= 2 {
			return true
		}
	}

	return false
}

// levenshteinDistance calculates edit distance between two strings
func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			del := curr[j-1] + 1
			ins := prev[j] + 1
			sub := prev[j-1] + cost
			curr[j] = minInt(del, minInt(ins, sub))
		}
		copy(prev, curr)
	}
	return prev[len(b)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *Registry) GetByID(id string) (Skill, bool) {
	if r.loader == nil {
		return Skill{}, false
	}
	return r.loader.GetByID(id)
}
