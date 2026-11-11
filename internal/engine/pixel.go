package engine

import (
	"container/list"
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"asteria/internal/img"
	"asteria/internal/ops"
	"asteria/internal/session"
	"asteria/internal/skills"
)

// Image chains on the pixel core.
//
// Every step still writes a real file (that is what previews, sizes and
// export use), but the next image step never decodes that file: it takes the
// lossless working image from this cache, or replays the chain in memory
// from the last real file (the original, or the output of a non-pixel step
// such as Compress). Quality is lost once, when a step is encoded, instead of
// once per step.

const pixCacheBudget = 640 << 20

type pixEntry struct {
	key   string
	m     *img.Image
	stat  string // file whose mtime/size must match for the entry to be valid
	mod   time.Time
	fsize int64
	el    *list.Element
}

type pixCache struct {
	mu    sync.Mutex
	used  int64
	items map[string]*pixEntry
	order *list.List // front = most recent
}

func newPixCache() *pixCache {
	return &pixCache{items: map[string]*pixEntry{}, order: list.New()}
}

func (c *pixCache) get(key string) *img.Image {
	c.mu.Lock()
	e, ok := c.items[key]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	st, err := os.Stat(e.stat)
	if err != nil || !st.ModTime().Equal(e.mod) || st.Size() != e.fsize {
		c.drop(key)
		return nil
	}
	c.mu.Lock()
	c.order.MoveToFront(e.el)
	c.mu.Unlock()
	return e.m
}

func (c *pixCache) put(key, statPath string, m *img.Image) {
	st, err := os.Stat(statPath)
	if err != nil || m.Bytes() > pixCacheBudget/2 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.items[key]; ok {
		c.used -= old.m.Bytes()
		c.order.Remove(old.el)
	}
	e := &pixEntry{key: key, m: m, stat: statPath, mod: st.ModTime(), fsize: st.Size()}
	e.el = c.order.PushFront(e)
	c.items[key] = e
	c.used += m.Bytes()
	for c.used > pixCacheBudget && c.order.Len() > 1 {
		last := c.order.Back().Value.(*pixEntry)
		c.order.Remove(last.el)
		delete(c.items, last.key)
		c.used -= last.m.Bytes()
	}
}

func (c *pixCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.Remove(e.el)
		delete(c.items, key)
		c.used -= e.m.Bytes()
	}
}

// pixelStep resolves a recorded step to its Pixel handler, if it has one.
func (e *Engine) pixelStep(st session.Step) (ops.Handler, skills.Skill, bool) {
	sk, ok := e.opts.Registry.GetByID(st.SkillID)
	if !ok || sk.Executor.Type != "native" || !ops.IsImageType(st.Type) {
		return ops.Handler{}, sk, false
	}
	h, ok := ops.Lookup(sk.Executor.Handler)
	if !ok || h.Pixel == nil {
		return ops.Handler{}, sk, false
	}
	return h, sk, true
}

// working returns the lossless working image for path, which is the
// original or one of f's step outputs (or a pipeline temp file).
func (e *Engine) working(ctx context.Context, f *session.File, path string) (*img.Image, error) {
	if m := e.pix.get(path); m != nil {
		return m, nil
	}
	steps := f.Steps()
	idx := -1
	for i, st := range steps {
		if st.Path == path {
			idx = i
		}
	}
	if idx < 0 {
		m, err := ops.DecodeWorking(path)
		if err != nil {
			return nil, err
		}
		e.pix.put(path, path, m)
		return m, nil
	}
	// Walk back to the nearest cached result or real (non-pixel) file.
	var base *img.Image
	k := idx
	for ; k >= 0; k-- {
		if m := e.pix.get(steps[k].Path); m != nil {
			base = m
			break
		}
		if _, _, ok := e.pixelStep(steps[k]); !ok {
			m, err := ops.DecodeWorking(steps[k].Path)
			if err != nil {
				return nil, err
			}
			base = m
			break
		}
	}
	if base == nil {
		orig := f.Original().Path
		if m := e.pix.get(orig); m != nil {
			base = m
		} else {
			m, err := ops.DecodeWorking(orig)
			if err != nil {
				return nil, err
			}
			e.pix.put(orig, orig, m)
			base = m
		}
	}
	for i := k + 1; i <= idx; i++ {
		h, sk, _ := e.pixelStep(steps[i])
		m, err := ops.RunPixel(ctx, h, base, ops.Merge(sk.Executor.With, steps[i].Params))
		if err != nil {
			return nil, fmt.Errorf("replaying %s: %w", sk.Name, err)
		}
		base = m
		e.pix.put(steps[i].Path, steps[i].Path, m)
	}
	return base, nil
}

// runPixel applies a Pixel handler for one step and writes its file.
func (e *Engine) runPixel(ctx context.Context, f *session.File, h ops.Handler, args ops.Args, inPath, outPath string, progress func(float64)) error {
	src, err := e.working(ctx, f, inPath)
	if err != nil {
		return err
	}
	progress(0.25)
	out, err := ops.RunPixel(ctx, h, src, args)
	if err != nil {
		return err
	}
	progress(0.6)
	if err := ops.EncodeWorking(out, outPath); err != nil {
		return err
	}
	e.pix.put(outPath, outPath, out)
	return nil
}

// Preview runs an image skill on a reduced copy of the file's current result
// and returns the encoded preview (JPEG, or PNG with transparency). Nothing
// is written to the tray. maxSide bounds the proxy's long side.
func (e *Engine) Preview(ctx context.Context, fileID, skillID string, params map[string]any, maxSide int) ([]byte, string, error) {
	f, ok := e.opts.Store.Get(fileID)
	if !ok {
		return nil, "", ops.Userf("that file is no longer in the tray")
	}
	sk, ok := e.opts.Registry.GetByID(skillID)
	if !ok || sk.Executor.Type != "native" {
		return nil, "", ops.Userf("no preview for this skill")
	}
	h, ok := ops.Lookup(sk.Executor.Handler)
	if !ok || h.Pixel == nil {
		return nil, "", ops.Userf("no preview for this skill")
	}
	cur := f.Current()
	if !ops.IsImageType(cur.Type) || !sk.Accepts(cur.Type) {
		return nil, "", ops.Userf("no preview for this file")
	}
	if maxSide < 64 || maxSide > 2400 {
		maxSide = 1200
	}
	key := fmt.Sprintf("%s#proxy%d", cur.Path, maxSide)
	proxy := e.pix.get(key)
	if proxy == nil {
		full, err := e.working(ctx, f, cur.Path)
		if err != nil {
			return nil, "", err
		}
		proxy = img.Proxy(full, maxSide)
		e.pix.put(key, cur.Path, proxy)
	}
	out, err := ops.RunPixel(ctx, h, proxy, ops.Merge(sk.Executor.With, params))
	if err != nil {
		return nil, "", err
	}
	return ops.PreviewBytes(out)
}
