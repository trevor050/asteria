package ops

import (
	"bytes"
	"context"
	"os"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/svg"
)

// Vector family. svg.optimize minifies an SVG with tdewolff/minify: comments,
// whitespace, redundant attributes and long number formats go; the viewBox,
// ids and structure stay, so the drawing renders the same. If minifying does
// not make the file smaller the original bytes are written unchanged.

func init() {
	register(Handler{Name: "svg.optimize", Run: svgOptimize})
}

func svgMinifier() *minify.M {
	m := minify.New()
	m.AddFunc("image/svg+xml", svg.Minify)
	m.AddFunc("text/css", css.Minify)
	return m
}

func svgOptimize(_ context.Context, job Job) error {
	st, err := os.Stat(job.Input())
	if err != nil {
		return err
	}
	if st.Size() > 128<<20 {
		return Userf("%s is too large to optimize (limit 128 MB)", job.Name(0))
	}
	in, err := os.ReadFile(job.Input())
	if err != nil {
		return err
	}
	if !bytes.Contains(in, []byte("<svg")) {
		return Userf("%s does not look like an SVG file", job.Name(0))
	}
	out, err := svgMinifier().Bytes("image/svg+xml", in)
	if err != nil {
		return Userf("could not optimize %s: %v", job.Name(0), err)
	}
	if len(out) >= len(in) {
		out = in
	}
	if err := os.WriteFile(job.Output, out, 0o644); err != nil {
		return err
	}
	job.Progress(1)
	return nil
}
