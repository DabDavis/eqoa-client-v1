package main

import (
	"image"
	"image/png"
	"log"
	"os"
	"sort"

	rl "github.com/gen2brain/raylib-go/raylib"
	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// loadESFFont loads the EQOA bitmap font from an ObjFile and returns an rl.Font.
func loadESFFont(file *esf.ObjFile) (rl.Font, bool) {
	if file == nil {
		return rl.Font{}, false
	}

	root, err := file.Root()
	if err != nil {
		return rl.Font{}, false
	}

	obj, err := file.GetObject(root)
	if err != nil || obj == nil {
		return rl.Font{}, false
	}
	font, ok := obj.(*esf.Font)
	if !ok || len(font.Glyphs) == 0 {
		return rl.Font{}, false
	}

	// Generate atlas
	atlasImg, rects := font.Atlas()
	if atlasImg == nil {
		return rl.Font{}, false
	}

	// Upload atlas to GPU — nearest-neighbor for crisp pixel-art bitmap font
	tex := imageToTexture(atlasImg)
	rl.SetTextureFilter(tex, rl.FilterBilinear)

	// Build rl.Font with glyph info
	numGlyphs := len(rects)
	esfFontRecs = make([]rl.Rectangle, numGlyphs)
	esfFontGlyphs = make([]rl.GlyphInfo, numGlyphs)

	// Sort by char code for consistent glyph lookup
	sortedCodes := make([]uint16, 0, numGlyphs)
	for code := range rects {
		sortedCodes = append(sortedCodes, code)
	}
	sort.Slice(sortedCodes, func(i, j int) bool { return sortedCodes[i] < sortedCodes[j] })

	i := 0
	for _, code := range sortedCodes {
		rect := rects[code]
		esfFontRecs[i] = rl.Rectangle{
			X:      float32(rect.X),
			Y:      float32(rect.Y),
			Width:  float32(rect.W),
			Height: float32(rect.H),
		}
		esfFontGlyphs[i] = rl.GlyphInfo{
			Value:    int32(code),
			OffsetX:  0,
			OffsetY:  0,
			AdvanceX: int32(rect.W),
		}
		i++
	}

	rlFont := rl.Font{
		BaseSize:     int32(font.CharHeight),
		CharsCount:   int32(numGlyphs),
		CharsPadding: 0,
		Texture:      tex,
		Recs:         &esfFontRecs[0],
		Chars:        &esfFontGlyphs[0],
	}

	log.Printf("font: EQOA %d glyphs, %dpx, atlas %dx%d",
		numGlyphs, font.CharHeight, atlasImg.Bounds().Dx(), atlasImg.Bounds().Dy())

	return rlFont, true
}

// Package-level slices prevent GC of data referenced by rl.Font pointers.
var esfFontRecs []rl.Rectangle
var esfFontGlyphs []rl.GlyphInfo

// imageToTexture uploads an NRGBA image to the GPU via temp PNG file.
func imageToTexture(img *image.NRGBA) rl.Texture2D {
	tmpPath := os.TempDir() + "/eqoa_font_atlas.png"
	f, err := os.Create(tmpPath)
	if err != nil {
		return rl.Texture2D{}
	}
	png.Encode(f, img)
	f.Close()
	tex := rl.LoadTexture(tmpPath)
	os.Remove(tmpPath)
	return tex
}
