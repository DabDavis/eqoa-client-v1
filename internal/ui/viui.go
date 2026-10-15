package ui

import (
	"log"
	"path/filepath"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// VIUI is the PS2-faithful UI rendering system.
// It loads all texture sheets from UI.ESF and uses the 252-entry
// texture table to look up source rectangles for each TexID.
type VIUI struct {
	sheets map[int]rl.Texture2D // sheet index → GPU texture
	ok     bool                 // true if at least sheet 1 loaded
}

// pageDictIDs maps PS2 VIUI page numbers (1-36) to surface DictIDs.
// Derived from DATA/TEXTURES.TXT page names hashed through VIHashResourceID
// with path template "P:\studio\eqps2\Game_Assets\UI\<name>.tga".
// Pages 24-26 (login bars) and 31-34 (login/status) are in other ESF files.
var pageDictIDs = map[int]int32{
	1:  -127886075,  // sheet1 (512x512 main atlas)
	2:  1703663189,  // borders_horiz
	3:  -1713517732, // borders_vert
	4:  -209548064,  // parchment_tile
	5:  -1169246912, // blue_tile
	6:  1584584898,  // stone_dark_tile
	7:  1834033905,  // stone_gray_tile
	8:  -430267892,  // stone_light_tile
	9:  -983392804,  // black
	10: -1845531800, // stone_indent_left_tile
	11: -1676554630, // stone_indent_top_tile
	12: -263418009,  // stone_indent_right_tile
	13: 590772992,   // stone_indent_bottom_tile
	14: 1582355568,  // parchment_left_tile
	15: -1912675630, // parchment_top_tile
	16: 2113228415,  // parchment_right_tile
	17: -1601158072, // parchment_bottom_tile
	18: 1316517018,  // sliderbar_tile
	19: -1189626108, // title_tile
	20: -579442169,  // target_name_tile
	21: -1816943963, // spellname_tile
	22: 2098338530,  // field_tile
	23: 1184188243,  // field_dark_tile
	// 24-26: login_bar_* (in PREGAME.ESF, not UI.ESF)
	27: -610629621,  // parchment_indent_tile
	28: 760190451,   // belt_vert_tile
	29: 432546354,   // belt_horiz_tile
	30: 1302437845,  // sliderbar_horiz_tile
	// 31: LoginPlaqueTile (dead reference)
	// 32-34: st* (in STATUI.CSF)
	35: 1950292963,  // frontiers
	36: -1256796368, // loading_please_wait
}

// NewVIUI opens UI.ESF from assetsDir and loads texture sheets.
func NewVIUI(assetsDir string) *VIUI {
	uiPath := filepath.Join(assetsDir, "UI.ESF")
	uiFile, err := esf.Open(uiPath)
	if err != nil {
		log.Printf("VIUI: UI.ESF open: %v", err)
		return &VIUI{sheets: make(map[int]rl.Texture2D)}
	}
	return NewVIUIFromFile(uiFile)
}

// NewVIUIFromFile loads texture sheets from a pre-opened UI.ESF ObjFile,
// matching the PS2's TEXTURES.TXT page→surface mapping.
func NewVIUIFromFile(uiFile *esf.ObjFile) *VIUI {
	v := &VIUI{
		sheets: make(map[int]rl.Texture2D),
	}

	if err := uiFile.BuildDictionary(); err != nil {
		log.Printf("VIUI: UI.ESF dict: %v", err)
		return v
	}

	// Load each page by DictID lookup (matches PS2 VIUI::Init behavior).
	for page, dictID := range pageDictIDs {
		obj, err := uiFile.FindObject(dictID)
		if err != nil || obj == nil {
			continue
		}
		surf, ok := obj.(*esf.Surface)
		if !ok || surf == nil || surf.Image == nil {
			continue
		}

		// Upload alpha unchanged: the palette already holds 0..255 and the PS2
		// halves it at draw time (a blended layer's vertex alpha is 64, not 128).
		rlImg := rl.NewImageFromImage(surf.Image)
		tex := rl.LoadTextureFromImage(rlImg)
		rl.UnloadImage(rlImg)

		rl.SetTextureWrap(tex, rl.WrapRepeat)
		rl.SetTextureFilter(tex, rl.FilterBilinear)

		v.sheets[page] = tex
	}

	v.ok = len(v.sheets) > 0
	log.Printf("VIUI: loaded %d/%d sheets from UI.ESF", len(v.sheets), len(pageDictIDs))
	return v
}

// OK returns true if at least one texture sheet loaded successfully.
func (v *VIUI) OK() bool {
	return v.ok
}

// Cleanup unloads all GPU textures.
func (v *VIUI) Cleanup() {
	for _, tex := range v.sheets {
		rl.UnloadTexture(tex)
	}
	v.sheets = nil
}

// Sheet returns the GPU texture for a sheet index, or a zero texture if not loaded.
func (v *VIUI) Sheet(id int) (rl.Texture2D, bool) {
	tex, ok := v.sheets[id]
	return tex, ok
}

// entry returns the table entry for a TexID, or nil if out of range / empty.
func entry(texID int) *UITexEntry {
	if texID < 1 || texID >= len(uiTexTable) {
		return nil
	}
	e := &uiTexTable[texID]
	if e.Width == 0 || e.Height == 0 {
		return nil
	}
	return e
}

// srcRect builds the source rectangle from a table entry,
// respecting the Flags field (0x01 = flip H, 0x02 = flip V).
func srcRect(e *UITexEntry) rl.Rectangle {
	w := float32(e.Width)
	h := float32(e.Height)
	if e.Flags&0x01 != 0 {
		w = -w // flip horizontally
	}
	if e.Flags&0x02 != 0 {
		h = -h // flip vertically
	}
	return rl.Rectangle{
		X: float32(e.SrcX), Y: float32(e.SrcY),
		Width: w, Height: h,
	}
}

// Blit draws a TexID at (x, y) at its native size.
func (v *VIUI) Blit(texID int, x, y float32, tint rl.Color) {
	e := entry(texID)
	if e == nil {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	src := srcRect(e)
	dst := rl.Rectangle{X: x, Y: y, Width: float32(e.Width), Height: float32(e.Height)}
	rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
}

// BlitEx draws a TexID stretched to (w, h) at position (x, y).
func (v *VIUI) BlitEx(texID int, x, y, w, h float32, tint rl.Color) {
	e := entry(texID)
	if e == nil {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	src := srcRect(e)
	dst := rl.Rectangle{X: x, Y: y, Width: w, Height: h}
	rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
}

// BlitExFlipH draws a TexID stretched to (w, h) at (x, y), flipped horizontally.
func (v *VIUI) BlitExFlipH(texID int, x, y, w, h float32, tint rl.Color) {
	e := entry(texID)
	if e == nil {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	src := srcRect(e)
	src.Width = -src.Width // flip horizontal
	dst := rl.Rectangle{X: x, Y: y, Width: w, Height: h}
	rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
}

// BlitRot draws a TexID at (x, y) with size (w, h) rotated around its center.
func (v *VIUI) BlitRot(texID int, x, y, w, h, rotation float32, tint rl.Color) {
	e := entry(texID)
	if e == nil {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	src := srcRect(e)
	origin := rl.Vector2{X: w / 2, Y: h / 2}
	dst := rl.Rectangle{X: x + origin.X, Y: y + origin.Y, Width: w, Height: h}
	rl.DrawTexturePro(tex, src, dst, origin, rotation, tint)
}

// BlitHorz tiles a TexID horizontally for pixelCount pixels, clipping the last tile.
// Used for tiling horizontal edges.
func (v *VIUI) BlitHorz(texID int, x, y float32, pixelCount int, tint rl.Color) {
	e := entry(texID)
	if e == nil || pixelCount <= 0 {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	tw := float32(e.Width)
	th := float32(e.Height)
	remaining := float32(pixelCount)
	cx := x
	for remaining > 0 {
		drawW := tw
		src := srcRect(e)
		if remaining < tw {
			// Clip last tile: reduce source width proportionally.
			frac := remaining / tw
			src.Width *= frac
			drawW = remaining
		}
		dst := rl.Rectangle{X: cx, Y: y, Width: drawW, Height: th}
		rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
		cx += drawW
		remaining -= drawW
	}
}

// BlitVert tiles a TexID vertically for pixelCount pixels, clipping the last tile.
// Used for tiling vertical edges.
func (v *VIUI) BlitVert(texID int, x, y float32, pixelCount int, tint rl.Color) {
	e := entry(texID)
	if e == nil || pixelCount <= 0 {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	tw := float32(e.Width)
	th := float32(e.Height)
	remaining := float32(pixelCount)
	cy := y
	for remaining > 0 {
		drawH := th
		src := srcRect(e)
		if remaining < th {
			frac := remaining / th
			src.Height *= frac
			drawH = remaining
		}
		dst := rl.Rectangle{X: x, Y: cy, Width: tw, Height: drawH}
		rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
		cy += drawH
		remaining -= drawH
	}
}

// BlitRect tiles a TexID in a 2D grid to fill a rectangle of (w × h) pixels.
// Used for filling frame interiors.
func (v *VIUI) BlitRect(texID int, x, y, w, h float32, tint rl.Color) {
	e := entry(texID)
	if e == nil || w <= 0 || h <= 0 {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	tw := float32(e.Width)
	th := float32(e.Height)
	for cy := y; cy < y+h; cy += th {
		drawH := th
		srcH := float32(e.Height)
		if cy+drawH > y+h {
			drawH = y + h - cy
			srcH = srcH * (drawH / th)
		}
		for cx := x; cx < x+w; cx += tw {
			drawW := tw
			srcW := float32(e.Width)
			if cx+drawW > x+w {
				drawW = x + w - cx
				srcW = srcW * (drawW / tw)
			}
			src := rl.Rectangle{
				X: float32(e.SrcX), Y: float32(e.SrcY),
				Width: srcW, Height: srcH,
			}
			if e.Flags&0x01 != 0 {
				src.Width = -src.Width
			}
			if e.Flags&0x02 != 0 {
				src.Height = -src.Height
			}
			dst := rl.Rectangle{X: cx, Y: cy, Width: drawW, Height: drawH}
			rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
		}
	}
}

// BlitBar draws a bar container + fill clipped by fraction (0.0-1.0).
// The fill is drawn inside the container with 1px padding.
func (v *VIUI) BlitBar(containerTex, fillTex int, x, y, w, h, frac float32, tint rl.Color) {
	// Container background
	v.BlitEx(containerTex, x, y, w, h, tint)

	// Fill clipped by fraction
	if frac <= 0 {
		return
	}
	if frac > 1 {
		frac = 1
	}
	fe := entry(fillTex)
	if fe == nil {
		return
	}
	tex, ok := v.sheets[fe.Sheet]
	if !ok {
		return
	}
	pad := float32(1)
	innerW := w - pad*2
	fillW := innerW * frac
	// Clip source rect width proportionally
	src := srcRect(fe)
	src.Width *= frac
	dst := rl.Rectangle{X: x + pad, Y: y + pad, Width: fillW, Height: h - pad*2}
	rl.DrawTexturePro(tex, src, dst, rl.Vector2{}, 0, tint)
}

// BlitFrame is VIUI::BlitFrame: the edges between the corners, then the
// corners. Every piece is placed by the CORNERS' sizes — the bottom edge at
// the bottom-left corner's height, the right edge at the top-right corner's
// width — and drawn with its own flip flags.
func (v *VIUI) BlitFrame(x, y, w, h float32, fp UIFrameParam, tint rl.Color) {
	tl := entry(fp.TLCorner)
	tr := entry(fp.TRCorner)
	br := entry(fp.BRCorner)
	bl := entry(fp.BLCorner)
	if tl == nil || tr == nil || bl == nil || br == nil {
		return
	}
	tlW, tlH := float32(tl.Width), float32(tl.Height)
	trW, trH := float32(tr.Width), float32(tr.Height)
	brW, brH := float32(br.Width), float32(br.Height)
	blW, blH := float32(bl.Width), float32(bl.Height)
	f := fp.Flip

	v.tilePiece(fp.TopEdge, x+tlW, y, w-tlW-trW, true, f.Top, tint)
	v.tilePiece(fp.BottomEdge, x+blW, y+h-blH, w-blW-brW, true, f.Bottom, tint)
	v.tilePiece(fp.LeftEdge, x, y+tlH, h-tlH-blH, false, f.Left, tint)
	v.tilePiece(fp.RightEdge, x+w-trW, y+trH, h-trH-brH, false, f.Right, tint)

	v.blitPiece(fp.TLCorner, x, y, tlW, tlH, f.TL, tint)
	v.blitPiece(fp.TRCorner, x+w-trW, y, trW, trH, f.TR, tint)
	v.blitPiece(fp.BLCorner, x, y+h-blH, blW, blH, f.BL, tint)
	v.blitPiece(fp.BRCorner, x+w-brW, y+h-brH, brW, brH, f.BR, tint)
}

// blitPiece draws the top-left (w, h) of a TexID at (x, y) with the given
// flip flags in place of the atlas entry's own (VIUI::BlitEx): a short last
// tile is cropped, not squeezed.
func (v *VIUI) blitPiece(texID int, x, y, w, h float32, flags uint8, tint rl.Color) {
	e := entry(texID)
	if e == nil || w <= 0 || h <= 0 {
		return
	}
	tex, ok := v.sheets[e.Sheet]
	if !ok {
		return
	}
	// The texels from the entry's origin, mirrored in place when flipped —
	// raylib's negative source size, as the raster's swapped UVs.
	src := rl.Rectangle{X: float32(e.SrcX), Y: float32(e.SrcY), Width: w, Height: h}
	if flags&1 != 0 {
		src.Width = -w
	}
	if flags&2 != 0 {
		src.Height = -h
	}
	rl.DrawTexturePro(tex, src, rl.Rectangle{X: x, Y: y, Width: w, Height: h}, rl.Vector2{}, 0, tint)
}

// tilePiece repeats a TexID along an edge for n pixels — VIUI::BlitHorz /
// BlitVert — cropping the last tile.
func (v *VIUI) tilePiece(texID int, x, y, n float32, horizontal bool, flags uint8, tint rl.Color) {
	e := entry(texID)
	if e == nil || n <= 0 {
		return
	}
	tw, th := float32(e.Width), float32(e.Height)
	for done := float32(0); done < n; {
		if horizontal {
			step := min(tw, n-done)
			v.blitPiece(texID, x+done, y, step, th, flags, tint)
			done += step
		} else {
			step := min(th, n-done)
			v.blitPiece(texID, x, y+done, tw, step, flags, tint)
			done += step
		}
	}
}

// BlitFillFrame is VIUI::BlitFillFrame: the fill tiled inside the top-left
// and bottom-right CORNERS (not the edges), then the frame over it.
func (v *VIUI) BlitFillFrame(x, y, w, h float32, fp UIFrameParam, fillTex int, tint rl.Color) {
	tl := entry(fp.TLCorner)
	br := entry(fp.BRCorner)
	if tl == nil || br == nil || entry(fillTex) == nil {
		return
	}
	tlW, tlH := float32(tl.Width), float32(tl.Height)
	brW, brH := float32(br.Width), float32(br.Height)
	if fw, fh := w-tlW-brW, h-tlH-brH; fw > 0 && fh > 0 {
		v.BlitRect(fillTex, x+tlW, y+tlH, fw, fh, tint)
	}
	v.BlitFrame(x, y, w, h, fp, tint)
}

// TexSize returns the native width and height for a TexID.
func (v *VIUI) TexSize(texID int) (int, int) {
	e := entry(texID)
	if e == nil {
		return 0, 0
	}
	return e.Width, e.Height
}

