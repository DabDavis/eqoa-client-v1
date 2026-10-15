package ui

// UIFrameParam defines a 9-slice frame by TexIDs for its 8 border pieces —
// VIUIFrameParam. The fill is specified separately (different TexID for each
// frame style).
type UIFrameParam struct {
	TLCorner, TopEdge    int // TexIDs
	TRCorner, RightEdge  int
	BRCorner, BottomEdge int
	BLCorner, LeftEdge   int
	// Flip is each piece's flags: 1 mirrors it left-right, 2 top-bottom.
	// They replace the atlas entry's own flags (VIUI::BlitEx).
	Flip FrameFlips
}

// FrameFlips holds a frame's per-piece flip flags.
type FrameFlips struct {
	TL, Top, TR, Right, BR, Bottom, BL, Left uint8
}

// Frame presets — each defines the 8 border TexIDs for a 9-slice frame style.
var (
	// FramePanel — standard stone panel (20px corners, 18/19px edges):
	// VIUIWindow::Draw's fp[1], which a window draws with fill 35 inside its
	// thin outer frame. Its left edge is 47 and its right 52 (they were
	// swapped).
	FramePanel = UIFrameParam{
		TLCorner: 44, TopEdge: 45,
		TRCorner: 46, RightEdge: 52,
		BRCorner: 50, BottomEdge: 49,
		BLCorner: 48, LeftEdge: 47,
	}
	FillPanel = 35 // fp[1]'s fill in VIUIWindow::Draw (51 is the plain window's rect)

	// FrameOrnate — ornate gold/stone frame (26px corners, 26px edges).
	// Used for character sheet, quest log, map borders.
	FrameOrnate = UIFrameParam{
		TLCorner: 92, TopEdge: 93,
		TRCorner: 94, RightEdge: 95,
		BRCorner: 100, BottomEdge: 99,
		BLCorner: 98, LeftEdge: 97,
	}
	FillOrnate = 96 // ornate fill (64×64 dark tile)

	// FrameThin — thin line frame (11px corners, 11px edges): VIUIWindow::
	// Draw's fp[0]. One corner piece, 36, at all four corners, mirrored on
	// the right; 38 and 43 are STUDS (the unused fp[2]), not corners.
	FrameThin = UIFrameParam{
		TLCorner: 36, TopEdge: 37,
		TRCorner: 36, RightEdge: 42,
		BRCorner: 36, BottomEdge: 37,
		BLCorner: 36, LeftEdge: 42,
		Flip: FrameFlips{TR: 1, Right: 1, BR: 1},
	}
	FillThin  = 35 // thin fill (64×64 parchment)
	FillBlack = 61 // black fill (32×32, page 9)

	// FrameLine — minimal 2px line frame: VIUIWindow::Draw's fp[3]. The
	// bottom is the top edge flipped top-bottom and the right the left edge
	// mirrored.
	// Used for chat box, subtle borders.
	FrameLine = UIFrameParam{
		TLCorner: 101, TopEdge: 102,
		TRCorner: 103, RightEdge: 104,
		BRCorner: 106, BottomEdge: 102,
		BLCorner: 105, LeftEdge: 104,
		Flip: FrameFlips{Right: 1, Bottom: 2},
	}
)
