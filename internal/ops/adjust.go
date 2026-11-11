package ops

import (
	"context"
	"fmt"
	"math"

	"asteria/internal/img"
)

// Color and tone family. Handlers are named "adjust.*"; skills live in
// skills/core/adjust. See IMAGE_SKILLS.md for the conventions.

func init() {
	register(Handler{Name: "adjust.saturate", Pixel: pixSaturate, Live: true, Video: videoSaturate})
}

// pixSaturate scales chroma in OKLCh, so lightness and hue stay put while
// colourfulness changes ("saturate" = +40%, "saturate 150%", "saturate -30").
func pixSaturate(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := args.Amount("amount", 1.4, 0, 4)
	if err != nil {
		return nil, err
	}
	if k == 1 {
		return m, nil
	}
	f := float32(k)
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		return img.OKLabToLinear(L, A*f, B*f)
	}), nil
}

func videoSaturate(args Args) (string, error) {
	k, err := args.Amount("amount", 1.4, 0, 3)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("eq=saturation=%.3f", math.Max(0, k)), nil
}
