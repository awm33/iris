package mediaworker

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Caption rendering (M7): the export burns captions in as Go-rendered PNGs
// composited with ffmpeg's `overlay` filter. Not `subtitles`/`drawtext`:
// those need libass/libfreetype, which the ffmpeg on PATH (and any slim
// deployment build) may not have — overlay is universal, and rendering
// text ourselves keeps user strings OUT of the filter graph entirely (the
// enable window is the only per-caption filter input, and it's numeric).
// The bundled Go Regular face keeps the worker hermetic: no fontconfig,
// no host font lookup.

var captionFace = sync.OnceValues(func() (*opentype.Font, error) {
	return opentype.Parse(goregular.TTF)
})

// renderCaptionPNG draws text as white-on-translucent-black, wrapped to fit
// frameW, sized relative to the frame (so draft and master look alike).
func renderCaptionPNG(text string, frameW int) ([]byte, error) {
	fnt, err := captionFace()
	if err != nil {
		return nil, fmt.Errorf("caption font: %w", err)
	}
	size := float64(frameW) / 34 // ≈38px at 1280 — broadcast-ish
	face, err := opentype.NewFace(fnt, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	defer face.Close()

	maxW := fixed.I(int(float64(frameW) * 0.82))
	lines := wrapToWidth(face, strings.TrimSpace(text), maxW)
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty caption")
	}

	metrics := face.Metrics()
	lineH := (metrics.Ascent + metrics.Descent).Ceil() + 2
	padX, padY := lineH/2, lineH/4
	textW := 0
	for _, ln := range lines {
		if w := font.MeasureString(face, ln).Ceil(); w > textW {
			textW = w
		}
	}
	imgW, imgH := textW+2*padX, len(lines)*lineH+2*padY
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{0, 0, 0, 168}), image.Point{}, draw.Src)

	d := &font.Drawer{Dst: img, Src: image.White, Face: face}
	for i, ln := range lines {
		w := font.MeasureString(face, ln)
		d.Dot = fixed.Point26_6{
			X: fixed.I((imgW - w.Ceil()) / 2),
			Y: fixed.I(padY+i*lineH) + metrics.Ascent,
		}
		d.DrawString(ln)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wrapToWidth greedily wraps on spaces; a single word wider than maxW gets
// its own line (overlay clamps to frame — never an error).
func wrapToWidth(face font.Face, text string, maxW fixed.Int26_6) []string {
	var lines []string
	var cur string
	for _, word := range strings.Fields(text) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if font.MeasureString(face, cand) <= maxW || cur == "" {
			cur = cand
			continue
		}
		lines = append(lines, cur)
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
