package preview

import (
	"bytes"
	"encoding/base64"
	"image/png"

	"github.com/disintegration/imaging"
)

func ImagePreview(path string, maxWidth int) (string, error) {
	img, err := imaging.Open(path)
	if err != nil {
		return "", err
	}
	if maxWidth > 0 && img.Bounds().Dx() > maxWidth {
		img = imaging.Resize(img, maxWidth, 0, imaging.Lanczos)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return "data:image/png;base64," + encoded, nil
}
