package ui

// UIFrameParam defines a 9-slice frame by TexIDs for its 8 border pieces.
// The fill is specified separately (different TexID for each frame style).
type UIFrameParam struct {
	TLCorner, TopEdge    int // TexIDs
	TRCorner, RightEdge  int
	BRCorner, BottomEdge int
	BLCorner, LeftEdge   int
}

// Frame presets — each defines the 8 border TexIDs for a 9-slice frame style.
var (
	// FramePanel — standard stone panel (20px corners, 18/19px edges).
	// Used for target HUD, inventory windows, dialog boxes.
	FramePanel = UIFrameParam{
		TLCorner: 44, TopEdge: 45,
		TRCorner: 46, RightEdge: 47,
		BRCorner: 50, BottomEdge: 49,
		BLCorner: 48, LeftEdge: 52,
	}
	FillPanel = 51 // panel fill (64×64 stone tile)

	// FrameOrnate — ornate gold/stone frame (26px corners, 26px edges).
	// Used for character sheet, quest log, map borders.
	FrameOrnate = UIFrameParam{
		TLCorner: 92, TopEdge: 93,
		TRCorner: 94, RightEdge: 95,
		BRCorner: 100, BottomEdge: 99,
		BLCorner: 98, LeftEdge: 97,
	}
	FillOrnate = 96 // ornate fill (64×64 dark tile)

	// FrameThin — thin line frame (11px corners, 11px edges).
	// Used for tooltips, hover panels.
	FrameThin = UIFrameParam{
		TLCorner: 36, TopEdge: 37,
		TRCorner: 38, RightEdge: 42,
		BRCorner: 43, BottomEdge: 37, // bottom reuses top edge
		BLCorner: 43, LeftEdge: 42,   // BL reuses BR, left reuses right
	}
	FillThin  = 35 // thin fill (64×64 parchment)
	FillBlack = 61 // black fill (32×32, page 9)

	// FrameLine — minimal 2px line frame.
	// Used for chat box, subtle borders.
	FrameLine = UIFrameParam{
		TLCorner: 101, TopEdge: 102,
		TRCorner: 103, RightEdge: 104,
		BRCorner: 106, BottomEdge: 102, // bottom reuses top edge
		BLCorner: 105, LeftEdge: 104,   // left reuses right edge
	}
)
