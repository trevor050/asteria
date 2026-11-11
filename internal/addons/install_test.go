package addons

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type fixture struct {
	srv   *httptest.Server
	body  []byte
	hits  atomic.Int32
	cutAt int64 // close the connection after this many bytes on the first request
	hold  chan struct{}
}

func zipWith(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func serve(t *testing.T, body []byte) *fixture {
	f := &fixture{body: body}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.hits.Add(1)
		if f.hold != nil {
			<-f.hold
		}
		start := int64(0)
		if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
			start, _ = strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"), 10, 64)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(f.body)-1, len(f.body)))
			w.WriteHeader(http.StatusPartialContent)
		}
		data := f.body[start:]
		if n == 1 && f.cutAt > 0 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data[:f.cutAt])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.Write(data)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func manager(t *testing.T, f *fixture, mutate func(*Artifact)) *Manager {
	t.Helper()
	sum := sha256.Sum256(f.body)
	a := Artifact{Version: "1.0", URL: f.srv.URL + "/tool.zip", Size: int64(len(f.body)), SHA256: hex.EncodeToString(sum[:]), Format: "zip", Bins: map[string]string{"tool": "pkg/bin/tool"}, UnpackedMB: 1}
	if mutate != nil {
		mutate(&a)
	}
	m := NewManager(t.TempDir(), &Manifest{Schema: 1, Engines: []Engine{{ID: "tool", Name: "Tool", Tools: []string{"tool"}, Platforms: map[string]Artifact{Platform(): a}}}})
	m.Client = f.srv.Client()
	m.CheckRuns = func(context.Context, string, Artifact) error { return nil }
	return m
}

func TestInstallVerifiesUnpacksAndRemoves(t *testing.T) {
	f := serve(t, zipWith(t, map[string]string{"pkg/bin/tool": "#!/bin/sh\necho tool\n", "pkg/README": "hi"}))
	m := manager(t, f, nil)
	var phases []string
	if err := m.Install(context.Background(), "tool", func(p Progress) { phases = append(phases, p.Phase) }); err != nil {
		t.Fatal(err)
	}
	p, ok := m.BinPath("tool")
	if !ok || !strings.HasPrefix(p, m.Root) {
		t.Fatalf("bin %q %v", p, ok)
	}
	if rec, ok := m.Installed("tool"); !ok || rec.Version != "1.0" || rec.Bytes == 0 {
		t.Fatalf("record %+v", rec)
	}
	if joined := strings.Join(phases, ","); !strings.Contains(joined, "download") || !strings.Contains(joined, "verify") || !strings.Contains(joined, "unpack") {
		t.Fatalf("phases %s", joined)
	}
	if left, _ := filepath.Glob(filepath.Join(m.Root, ".downloads", "*")); len(left) != 0 {
		t.Fatalf("download left behind: %v", left)
	}
	freed, err := m.Remove("tool")
	if err != nil || freed == 0 {
		t.Fatalf("remove: %d %v", freed, err)
	}
	if _, ok := m.BinPath("tool"); ok {
		t.Fatal("still installed after remove")
	}
}

func TestChecksumMismatchRefusesAndDeletes(t *testing.T) {
	f := serve(t, zipWith(t, map[string]string{"pkg/bin/tool": "x"}))
	m := manager(t, f, func(a *Artifact) { a.SHA256 = strings.Repeat("0", 64) })
	err := m.Install(context.Background(), "tool", nil)
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "checksum") {
		t.Fatalf("got %v", err)
	}
	if _, ok := m.Installed("tool"); ok {
		t.Fatal("installed despite a bad checksum")
	}
	if left, _ := filepath.Glob(filepath.Join(m.Root, ".downloads", "*")); len(left) != 0 {
		t.Fatalf("bad download kept: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(m.Root, ".staging", "*")); len(left) != 0 {
		t.Fatal("unpacked despite a bad checksum")
	}
}

func TestInterruptedDownloadResumes(t *testing.T) {
	body := zipWith(t, map[string]string{"pkg/bin/tool": strings.Repeat("abcdefgh", 40000)})
	f := serve(t, body)
	f.cutAt = int64(len(body) / 3)
	m := manager(t, f, nil)
	if err := m.Install(context.Background(), "tool", nil); err == nil {
		t.Fatal("first attempt should fail when the connection drops")
	}
	parts, _ := filepath.Glob(filepath.Join(m.Root, ".downloads", "*.part"))
	if len(parts) != 1 {
		t.Fatalf("partial download not kept: %v", parts)
	}
	if err := m.Install(context.Background(), "tool", nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, ok := m.BinPath("tool"); !ok {
		t.Fatal("not installed after resume")
	}
}

func TestCancelRemovesPartialDownload(t *testing.T) {
	f := serve(t, zipWith(t, map[string]string{"pkg/bin/tool": "x"}))
	f.hold = make(chan struct{})
	m := manager(t, f, nil)
	done := make(chan error, 1)
	go func() { done <- m.Install(context.Background(), "tool", nil) }()
	for f.hits.Load() == 0 {
	}
	m.Cancel("tool")
	close(f.hold)
	if err := <-done; !errors.Is(err, ErrCancelled) {
		t.Fatalf("got %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(m.Root, ".downloads", "*")); len(left) != 0 {
		t.Fatalf("partial kept after cancel: %v", left)
	}
}

func TestNotEnoughSpace(t *testing.T) {
	f := serve(t, zipWith(t, map[string]string{"pkg/bin/tool": "x"}))
	m := manager(t, f, nil)
	m.FreeSpace = func(string) (uint64, error) { return 1000, nil }
	err := m.Install(context.Background(), "tool", nil)
	if err == nil || !strings.Contains(err.Error(), "Not enough free space") {
		t.Fatalf("got %v", err)
	}
	if f.hits.Load() != 0 {
		t.Fatal("downloaded despite no space")
	}
}

func TestOfflineAndInsecure(t *testing.T) {
	f := serve(t, zipWith(t, map[string]string{"pkg/bin/tool": "x"}))
	m := manager(t, f, func(a *Artifact) { a.URL = "https://asteria-addon-test.invalid/tool.zip" })
	m.Client = &http.Client{}
	err := m.Install(context.Background(), "tool", nil)
	if err == nil || !strings.Contains(err.Error(), "Couldn't reach") {
		t.Fatalf("offline: %v", err)
	}
	m2 := manager(t, f, func(a *Artifact) { a.URL = "http://example.com/tool.zip" })
	if err := m2.Install(context.Background(), "tool", nil); err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("http: %v", err)
	}
	if _, err := ParseManifest([]byte(`{"schema":1,"engines":[{"id":"x","platforms":{"linux-amd64":{"url":"http://x","sha256":"` + strings.Repeat("a", 64) + `","size":1,"bins":{"x":"x"}}}}]}`)); err == nil {
		t.Fatal("manifest accepted an http URL")
	}
}

func TestUnpackRefusesEscapes(t *testing.T) {
	dst := t.TempDir()
	// zip slip
	z := filepath.Join(t.TempDir(), "evil.zip")
	os.WriteFile(z, zipWith(t, map[string]string{"../../escaped": "x"}), 0o644)
	if err := unpack(context.Background(), Artifact{Format: "zip"}, z, dst); err == nil {
		t.Fatal("zip slip accepted")
	}
	// tar with an absolute path and a symlink pointing out
	for _, h := range []*tar.Header{
		{Name: "/etc/evil", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../../outside"},
	} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(h)
		if h.Size > 0 {
			tw.Write([]byte("x"))
		}
		tw.Close()
		gz.Close()
		p := filepath.Join(t.TempDir(), "evil.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o644)
		if err := unpack(context.Background(), Artifact{Format: "tar.gz"}, p, dst); err == nil {
			t.Fatalf("accepted %s", h.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "escaped")); err == nil {
		t.Fatal("file written outside the target")
	}
}
