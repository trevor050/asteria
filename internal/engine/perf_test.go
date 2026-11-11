package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"asteria/internal/inspect"
)

// Performance check, not run in CI: ASTERIA_PERF_IMAGE=<24 MP jpeg>
// [ASTERIA_PERF_BATCH=<folder of photos>] go test -run Perf -v ./internal/engine
func TestPerfChainAndBatch(t *testing.T) {
	src := os.Getenv("ASTERIA_PERF_IMAGE")
	if src == "" {
		t.Skip("set ASTERIA_PERF_IMAGE")
	}
	e := newEnv(t, "")
	f, err := e.store.Add(src, func(path, typ string) inspect.Info { return inspect.Describe(context.Background(), nil, path, typ) })
	if err != nil {
		t.Fatal(err)
	}
	var peak uint64
	stop := make(chan struct{})
	go func() {
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&ms)
				if ms.HeapInuse > peak {
					peak = ms.HeapInuse
				}
			}
		}
	}()
	chain := []struct {
		id    string
		value any
	}{
		{"enhance", nil}, {"saturate", "+30"}, {"contrast", "+15"}, {"temperature", "+20"}, {"shadows", "+25"},
		{"sharpen", nil}, {"vignette", "40"}, {"crop", "16:9"}, {"film_grain", "15"}, {"resize", "50%"},
	}
	total := time.Duration(0)
	for _, c := range chain {
		sk, _ := e.reg.GetByID(c.id)
		params := map[string]any{}
		if c.value != nil && len(sk.Params) == 1 {
			params[sk.Params[0].Name] = c.value
		}
		t0 := time.Now()
		out, err := e.eng.Apply(context.Background(), []string{f.ID()}, c.id, params, nil)
		d := time.Since(t0)
		total += d
		if err != nil || len(out.Updated) != 1 {
			t.Fatalf("%s: %v %+v", c.id, err, out)
		}
		t.Logf("%-12s %6.0f ms", c.id, float64(d.Milliseconds()))
	}
	t.Logf("10-step chain total %.1f s", total.Seconds())
	// Preview on the result (what a live scrub costs once the proxy is warm).
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		if _, _, err := e.eng.Preview(context.Background(), f.ID(), "saturate", map[string]any{"amount": fmt.Sprintf("+%d", 10*i)}, 1400); err != nil {
			t.Fatal(err)
		}
		t.Logf("preview %d %4d ms", i, time.Since(t0).Milliseconds())
	}
	if dir := os.Getenv("ASTERIA_PERF_BATCH"); dir != "" {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
		ids := []string{}
		for _, p := range matches {
			bf, err := e.store.Add(p, func(path, typ string) inspect.Info { return inspect.Describe(context.Background(), nil, path, typ) })
			if err == nil {
				ids = append(ids, bf.ID())
			}
		}
		t0 := time.Now()
		out, err := e.eng.Apply(context.Background(), ids, "enhance", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("batch: Auto Enhance on %d photos in %.1f s (%d updated, %d failed)", len(ids), time.Since(t0).Seconds(), len(out.Updated), len(out.Failed))
	}
	close(stop)
	t.Logf("peak Go heap in use %.0f MB", float64(peak)/1e6)
}
