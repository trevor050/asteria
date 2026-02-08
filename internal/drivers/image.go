package drivers

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"math"
	"path/filepath"
	"strconv"

	"asteria/internal/skills"

	"github.com/disintegration/imaging"
)

type ImageDriver struct{}

func (d *ImageDriver) ID() string {
	return "image"
}

func (d *ImageDriver) Supports(skill skills.Skill) bool {
	return skill.Driver == d.ID()
}

func (d *ImageDriver) Execute(ctx context.Context, inputPath string, outputPath string, skill skills.Skill, params map[string]any, progress ProgressFunc) error {
	if progress != nil {
		progress(0.1)
	}
	img, err := imaging.Open(inputPath)
	if err != nil {
		return err
	}
	switch skill.ID {
	case "resize":
		percent := readFloat(params, "percent", 100)
		if percent <= 0 {
			return errors.New("resize percent must be greater than 0")
		}
		bounds := img.Bounds()
		width := int(math.Max(1, float64(bounds.Dx())*percent/100))
		height := int(math.Max(1, float64(bounds.Dy())*percent/100))
		img = imaging.Resize(img, width, height, imaging.Lanczos)
	case "grayscale":
		img = imaging.Grayscale(img)
	case "blur":
		radius := readFloat(params, "radius", 2.0)
		img = imaging.Blur(img, radius)
	case "compress":
	case "convert_to_jpeg":
	case "convert_to_png":
	default:
		return fmt.Errorf("unsupported image skill: %s", skill.ID)
	}
	if progress != nil {
		progress(0.6)
	}

	ext := filepath.Ext(outputPath)
	switch ext {
	case ".jpg", ".jpeg":
		quality := int(readFloat(params, "quality", 90))
		if quality < 40 {
			quality = 40
		}
		if quality > 100 {
			quality = 100
		}
		err = imaging.Save(img, outputPath, imaging.JPEGQuality(quality))
	case ".png":
		options := []imaging.EncodeOption{}
		if skill.ID == "compress" {
			options = append(options, imaging.PNGCompressionLevel(png.BestCompression))
		}
		err = imaging.Save(img, outputPath, options...)
	default:
		err = imaging.Save(img, outputPath)
	}
	if err != nil {
		return err
	}
	if progress != nil {
		progress(1.0)
	}
	return nil
}

func readFloat(params map[string]any, key string, fallback float64) float64 {
	if params == nil {
		return fallback
	}
	value, ok := params[key]
	if !ok {
		return fallback
	}
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case float32:
		return float64(v)
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}
