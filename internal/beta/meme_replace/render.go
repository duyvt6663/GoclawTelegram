package memereplace

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/disintegration/imaging"
)

type RectSpec struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type renderStats struct {
	Width       int
	Height      int
	Region      RectSpec
	KeyedPixels int
}

var templateRegion = RectSpec{X: 92, Y: 96, W: 456, H: 344}

func renderReplacement(input image.Image, fitMode string) ([]byte, renderStats, error) {
	template := memeTemplateImage()
	bounds := template.Bounds()
	out := image.NewRGBA(bounds)

	region := image.Rect(
		templateRegion.X,
		templateRegion.Y,
		templateRegion.X+templateRegion.W,
		templateRegion.Y+templateRegion.H,
	)
	draw.Draw(out, region, &image.Uniform{C: color.RGBA{R: 12, G: 14, B: 16, A: 255}}, image.Point{}, draw.Src)
	replacement := fitReplacement(input, region.Dx(), region.Dy(), fitMode)
	offset := image.Pt(
		region.Min.X+(region.Dx()-replacement.Bounds().Dx())/2,
		region.Min.Y+(region.Dy()-replacement.Bounds().Dy())/2,
	)
	draw.Draw(out, replacement.Bounds().Add(offset), replacement, replacement.Bounds().Min, draw.Over)

	keyed := chromaKeyTemplate(template)
	draw.Draw(out, bounds, keyed, bounds.Min, draw.Over)

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, renderStats{}, err
	}
	return buf.Bytes(), renderStats{
		Width:       bounds.Dx(),
		Height:      bounds.Dy(),
		Region:      templateRegion,
		KeyedPixels: countGreenPixels(template),
	}, nil
}

func fitReplacement(input image.Image, width, height int, mode string) image.Image {
	if mode == fitModeContain {
		return imaging.Fit(input, width, height, imaging.Lanczos)
	}
	return imaging.Fill(input, width, height, imaging.Center, imaging.Lanczos)
}

func memeTemplateImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 640, 640))
	fill(img, img.Bounds(), color.RGBA{R: 24, G: 25, B: 32, A: 255})
	fill(img, image.Rect(0, 0, 640, 76), color.RGBA{R: 235, G: 234, B: 228, A: 255})
	fill(img, image.Rect(0, 500, 640, 640), color.RGBA{R: 235, G: 234, B: 228, A: 255})
	fill(img, image.Rect(44, 66, 596, 486), color.RGBA{R: 41, G: 43, B: 50, A: 255})
	fill(img, image.Rect(60, 80, 580, 472), color.RGBA{R: 13, G: 16, B: 22, A: 255})

	green := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	fill(img, image.Rect(templateRegion.X, templateRegion.Y, templateRegion.X+templateRegion.W, templateRegion.Y+templateRegion.H), green)

	fill(img, image.Rect(70, 86, 570, 96), color.RGBA{R: 72, G: 75, B: 84, A: 255})
	fill(img, image.Rect(70, 440, 570, 456), color.RGBA{R: 72, G: 75, B: 84, A: 255})
	fill(img, image.Rect(52, 54, 588, 66), color.RGBA{R: 196, G: 62, B: 72, A: 255})
	fill(img, image.Rect(92, 520, 548, 548), color.RGBA{R: 34, G: 36, B: 43, A: 255})
	fill(img, image.Rect(130, 566, 510, 594), color.RGBA{R: 34, G: 36, B: 43, A: 255})
	return img
}

func chromaKeyTemplate(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := rgbaAt(src, x, y)
			if isGreenKey(c) {
				c = color.RGBA{}
			}
			dst.SetRGBA(x, y, c)
		}
	}
	return dst
}

func countGreenPixels(src image.Image) int {
	bounds := src.Bounds()
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if isGreenKey(rgbaAt(src, x, y)) {
				count++
			}
		}
	}
	return count
}

func isGreenKey(c color.RGBA) bool {
	return c.A > 0 && c.G > 120 && int(c.G) > int(c.R)*5/4 && int(c.G) > int(c.B)*5/4
}

func rgbaAt(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func fill(dst draw.Image, rect image.Rectangle, c color.Color) {
	draw.Draw(dst, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}
