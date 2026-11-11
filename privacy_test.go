package main

import (
	"context"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"asteria/internal/engine"
	"asteria/internal/inspect"
	"asteria/internal/session"
	"asteria/internal/skills"
	"asteria/internal/tools"
)

// Asteria's promise: files never leave the machine. Only the add-on
// installer may make outgoing connections (and only when the user clicks
// to install); the preview server only listens on localhost inside the
// app. This test fails if any other source file imports networking.
func TestOnlyTheAddonInstallerUsesTheNetwork(t *testing.T) {
	allowed := map[string]bool{
		"internal/addons/install.go": true, // downloads an add-on the user asked for
		"preview.go":                 true, // serves previews to the app's own window
	}
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "frontend" || strings.HasPrefix(d.Name(), ".")) && p != "." {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if (path == "net" || strings.HasPrefix(path, "net/http") || path == "net/rpc" || path == "crypto/tls") && !allowed[filepath.ToSlash(p)] {
				t.Errorf("%s imports %s; only the add-on installer may use the network", p, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type trapTransport struct{ hits *atomic.Int32 }

func (t trapTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.hits.Add(1)
	return nil, &net.OpError{Op: "dial", Err: os.ErrPermission}
}

// Every built-in skill that runs without an add-on, on real fixtures, with
// all Go-level network access trapped: nothing may try to connect.
func TestProcessingMakesNoConnections(t *testing.T) {
	var hits atomic.Int32
	oldT := http.DefaultTransport
	http.DefaultTransport = trapTransport{&hits}
	oldR := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		hits.Add(1)
		return nil, os.ErrPermission
	}}
	defer func() { http.DefaultTransport, net.DefaultResolver = oldT, oldR }()

	reg := skills.NewRegistry(skills.RegistryOptions{EmbeddedFS: os.DirFS("skills"), EmbeddedRoot: "core"})
	ws, err := session.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(ws)
	finder := tools.NewFinderWithDirs(nil)
	finder.Prefer(func(string) (string, bool) { return "", false })
	eng := engine.New(engine.Options{Registry: reg, Store: store, Tools: finder})
	fixtures, _ := filepath.Glob("testdata/fixtures/*")
	formats, _ := filepath.Glob("testdata/formats/*")
	ran := 0
	for _, src := range append(fixtures, formats...) {
		typ := skills.FileType(src, false)
		for _, sk := range reg.List() {
			if sk.IsMeta || sk.Combine || !sk.Accepts(typ) || len(sk.Requires) > 0 {
				continue
			}
			f, err := store.Add(src, func(p, ty string) inspect.Info { return inspect.Describe(context.Background(), finder, p, ty) })
			if err != nil {
				continue
			}
			_, _ = eng.Apply(context.Background(), []string{f.ID()}, sk.ID, nil, nil)
			store.Remove([]string{f.ID()})
			ran++
		}
	}
	if ran < 50 {
		t.Fatalf("only %d skill runs; fixtures missing?", ran)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("processing tried to use the network %d times", n)
	}
	t.Logf("%d skill runs, no connection attempts", ran)
}
