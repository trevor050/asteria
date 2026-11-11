package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sync/singleflight"

	"asteria/internal/inspect"
	"asteria/internal/session"
)

// previewHandler serves tray files to the webview so previews show the real
// output: /asteria/thumb/<id>/<rev>?s=480 renders an image thumbnail and
// /asteria/file/<id>/<rev> streams the current output (video, audio, PDF).
// Only files in the tray are reachable, by id; never arbitrary paths.
type previewHandler struct {
	store *session.Store
	group singleflight.Group
}

func (h *previewHandler) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/asteria/") {
			next.ServeHTTP(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/asteria/"), "/")
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		kind, id, rev := parts[0], parts[1], parts[2]
		f, ok := h.store.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		step := f.Current()
		if rev == "original" || rev == step.ID {
			if rev == "original" {
				step = f.Original()
			}
		} else {
			found := false
			for _, s := range f.Steps() {
				if s.ID == rev {
					step, found = s, true
				}
			}
			if !found {
				http.NotFound(w, r)
				return
			}
		}
		switch kind {
		case "thumb":
			h.thumb(w, r, f, step)
		case "file":
			if step.IsDir() {
				http.NotFound(w, r)
				return
			}
			file, err := os.Open(step.Path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
			http.ServeContent(w, r, "file"+step.Type, info.ModTime(), file)
		default:
			http.NotFound(w, r)
		}
	})
}

func (h *previewHandler) thumb(w http.ResponseWriter, r *http.Request, f *session.File, step session.Step) {
	size, _ := strconv.Atoi(r.URL.Query().Get("s"))
	if size <= 0 || size > 2400 {
		size = 480
	}
	cacheDir := filepath.Join(f.Dir(), "thumbs")
	key := step.ID + "-" + strconv.Itoa(size)
	v, err, _ := h.group.Do(f.ID()+key, func() (any, error) {
		for _, ext := range []string{".jpg", ".png"} {
			if b, err := os.ReadFile(filepath.Join(cacheDir, key+ext)); err == nil {
				return thumbData{b, mimeFor(ext)}, nil
			}
		}
		b, mime, err := inspect.Thumbnail(step.Path, size)
		if err != nil {
			return nil, err
		}
		ext := ".jpg"
		if mime == "image/png" {
			ext = ".png"
		}
		_ = os.MkdirAll(cacheDir, 0o700)
		_ = os.WriteFile(filepath.Join(cacheDir, key+ext), b, 0o600)
		return thumbData{b, mime}, nil
	})
	if err != nil {
		http.Error(w, "no preview", http.StatusUnsupportedMediaType)
		return
	}
	td := v.(thumbData)
	w.Header().Set("Content-Type", td.mime)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(td.data)
}

type thumbData struct {
	data []byte
	mime string
}

func mimeFor(ext string) string {
	if ext == ".png" {
		return "image/png"
	}
	return "image/jpeg"
}
