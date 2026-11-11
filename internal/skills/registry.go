package skills

import (
	"context"
	"io/fs"
	"sort"
)

type RegistryOptions struct {
	EmbeddedFS    fs.FS
	EmbeddedRoot  string
	DiskCoreRoot  string
	CommunityRoot string
}

// Registry holds the loaded skills and answers searches.
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

// List returns every loaded skill sorted by id.
func (r *Registry) List() []Skill {
	list := r.loader.List()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

func (r *Registry) StartHotReload(ctx context.Context) error { return r.loader.Watch(ctx) }

func (r *Registry) Changes() <-chan struct{} { return r.loader.Changes() }

// Reload re-reads every skill directory.
func (r *Registry) Reload() error { return r.loader.LoadAll() }

// Problems lists skill files that failed to load.
func (r *Registry) Problems() []string { return r.loader.Problems() }

// Search ranks skills for a command-bar query.
func (r *Registry) Search(query string, ctx Context) []Match {
	return r.ranker.Rank(r.List(), query, ctx)
}

func (r *Registry) GetByID(id string) (Skill, bool) { return r.loader.GetByID(id) }
