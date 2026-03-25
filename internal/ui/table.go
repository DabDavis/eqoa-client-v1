package ui

// UITexEntry describes one texture region in the PS2 VIUI system.
// Extracted from EE memory at 0x004EDAF8 (252 entries × 36 bytes each).
// PS2 struct: id(+0), page(+4), x(+8), y(+C), w(+10), h(+14), flags(+18=0), scale(+1C=1.0), mode(+20).
type UITexEntry struct {
	Sheet         int // texture page (1-36), mapped to DictIDs via TEXTURES.TXT
	SrcX, SrcY    int // source position within sheet
	Width, Height int // source region size
	Flags         int // PS2 "mode" field: 0=normal, 1=flip H, 2=flip V
}

// uiTexTable is the complete 252-entry PS2 GetUITexture lookup table.
// Index 0 is unused (the PS2 table is 1-indexed, TexID 0 = empty).
var uiTexTable = [252]UITexEntry{
	// [0] unused
	{},
	// [1]
	{Sheet: 1, SrcX: 207, SrcY: 203, Width: 114, Height: 44, Flags: 0x00},
	// [2]
	{Sheet: 1, SrcX: 61, SrcY: 354, Width: 22, Height: 22, Flags: 0x00},
	// [3]
	{Sheet: 1, SrcX: 83, SrcY: 354, Width: 21, Height: 22, Flags: 0x00},
	// [4]
	{Sheet: 1, SrcX: 100, SrcY: 354, Width: 22, Height: 22, Flags: 0x00},
	// [5]
	{Sheet: 1, SrcX: 127, SrcY: 354, Width: 22, Height: 22, Flags: 0x00},
	// [6] compass bezel
	{Sheet: 1, SrcX: 0, SrcY: 381, Width: 59, Height: 69, Flags: 0x00},
	// [7] HP bar fill (red)
	{Sheet: 1, SrcX: 123, SrcY: 189, Width: 55, Height: 6, Flags: 0x00},
	// [8] XP bar fill (gold)
	{Sheet: 1, SrcX: 123, SrcY: 195, Width: 55, Height: 6, Flags: 0x00},
	// [9] Power bar fill (blue)
	{Sheet: 1, SrcX: 123, SrcY: 201, Width: 55, Height: 6, Flags: 0x00},
	// [10]
	{Sheet: 1, SrcX: 307, SrcY: 328, Width: 29, Height: 44, Flags: 0x00},
	// [11]
	{Sheet: 1, SrcX: 322, SrcY: 107, Width: 65, Height: 44, Flags: 0x00},
	// [12]
	{Sheet: 1, SrcX: 386, SrcY: 107, Width: 58, Height: 44, Flags: 0x00},
	// [13]
	{Sheet: 1, SrcX: 445, SrcY: 107, Width: 55, Height: 44, Flags: 0x00},
	// [14]
	{Sheet: 1, SrcX: 321, SrcY: 151, Width: 55, Height: 44, Flags: 0x00},
	// [15]
	{Sheet: 1, SrcX: 376, SrcY: 151, Width: 74, Height: 44, Flags: 0x00},
	// [16]
	{Sheet: 1, SrcX: 451, SrcY: 151, Width: 51, Height: 44, Flags: 0x00},
	// [17]
	{Sheet: 1, SrcX: 321, SrcY: 195, Width: 59, Height: 44, Flags: 0x00},
	// [18]
	{Sheet: 1, SrcX: 380, SrcY: 195, Width: 73, Height: 44, Flags: 0x00},
	// [19]
	{Sheet: 1, SrcX: 321, SrcY: 239, Width: 92, Height: 44, Flags: 0x00},
	// [20]
	{Sheet: 1, SrcX: 454, SrcY: 195, Width: 58, Height: 44, Flags: 0x00},
	// [21]
	{Sheet: 1, SrcX: 418, SrcY: 239, Width: 84, Height: 44, Flags: 0x00},
	// [22]
	{Sheet: 1, SrcX: 321, SrcY: 283, Width: 48, Height: 44, Flags: 0x00},
	// [23]
	{Sheet: 1, SrcX: 369, SrcY: 283, Width: 70, Height: 44, Flags: 0x00},
	// [24]
	{Sheet: 1, SrcX: 441, SrcY: 283, Width: 71, Height: 44, Flags: 0x00},
	// [25]
	{Sheet: 1, SrcX: 93, SrcY: 307, Width: 13, Height: 25, Flags: 0x00},
	// [26]
	{Sheet: 20, SrcX: 0, SrcY: 0, Width: 32, Height: 25, Flags: 0x00},
	// [27]
	{Sheet: 1, SrcX: 93, SrcY: 333, Width: 13, Height: 20, Flags: 0x00},
	// [28]
	{Sheet: 21, SrcX: 0, SrcY: 0, Width: 32, Height: 20, Flags: 0x00},
	// [29]
	{Sheet: 1, SrcX: 4, SrcY: 300, Width: 36, Height: 31, Flags: 0x00},
	// [30] golden scroll border
	{Sheet: 1, SrcX: 0, SrcY: 290, Width: 44, Height: 9, Flags: 0x00},
	// [31] golden scroll border (flipped)
	{Sheet: 1, SrcX: 0, SrcY: 290, Width: 44, Height: 9, Flags: 0x02},
	// [32]
	{Sheet: 5, SrcX: 0, SrcY: 0, Width: 64, Height: 64, Flags: 0x00},
	// [33]
	{Sheet: 1, SrcX: 52, SrcY: 213, Width: 16, Height: 28, Flags: 0x00},
	// [34]
	{Sheet: 19, SrcX: 0, SrcY: 0, Width: 32, Height: 28, Flags: 0x00},
	// [35]
	{Sheet: 4, SrcX: 0, SrcY: 0, Width: 64, Height: 64, Flags: 0x00},
	// [36] thin TL corner
	{Sheet: 1, SrcX: 12, SrcY: 338, Width: 11, Height: 11, Flags: 0x00},
	// [37] thin top edge
	{Sheet: 2, SrcX: 0, SrcY: 0, Width: 64, Height: 11, Flags: 0x00},
	// [38] thin TR corner
	{Sheet: 1, SrcX: 24, SrcY: 338, Width: 15, Height: 11, Flags: 0x00},
	// [39]
	{Sheet: 1, SrcX: 107, SrcY: 293, Width: 11, Height: 18, Flags: 0x00},
	// [40]
	{Sheet: 2, SrcX: 0, SrcY: 11, Width: 64, Height: 18, Flags: 0x00},
	// [41]
	{Sheet: 1, SrcX: 119, SrcY: 293, Width: 15, Height: 18, Flags: 0x00},
	// [42] thin left edge
	{Sheet: 3, SrcX: 0, SrcY: 0, Width: 11, Height: 64, Flags: 0x00},
	// [43] thin BL corner
	{Sheet: 1, SrcX: 0, SrcY: 334, Width: 11, Height: 15, Flags: 0x00},
	// [44] panel TL corner
	{Sheet: 1, SrcX: 24, SrcY: 474, Width: 20, Height: 18, Flags: 0x00},
	// [45] panel top edge
	{Sheet: 15, SrcX: 0, SrcY: 0, Width: 64, Height: 18, Flags: 0x00},
	// [46] panel TR corner
	{Sheet: 1, SrcX: 45, SrcY: 474, Width: 20, Height: 18, Flags: 0x00},
	// [47] panel right edge
	{Sheet: 14, SrcX: 0, SrcY: 0, Width: 20, Height: 64, Flags: 0x00},
	// [48] panel BL corner
	{Sheet: 1, SrcX: 24, SrcY: 493, Width: 20, Height: 19, Flags: 0x00},
	// [49] panel bottom edge
	{Sheet: 17, SrcX: 0, SrcY: 0, Width: 64, Height: 19, Flags: 0x00},
	// [50] panel BR corner
	{Sheet: 1, SrcX: 45, SrcY: 493, Width: 20, Height: 19, Flags: 0x00},
	// [51] panel fill
	{Sheet: 8, SrcX: 0, SrcY: 0, Width: 64, Height: 64, Flags: 0x00},
	// [52] panel left edge
	{Sheet: 16, SrcX: 0, SrcY: 0, Width: 20, Height: 64, Flags: 0x00},
	// [53]
	{Sheet: 1, SrcX: 0, SrcY: 485, Width: 11, Height: 27, Flags: 0x00},
	// [54]
	{Sheet: 22, SrcX: 0, SrcY: 0, Width: 64, Height: 27, Flags: 0x00},
	// [55]
	{Sheet: 1, SrcX: 12, SrcY: 485, Width: 11, Height: 27, Flags: 0x00},
	// [56]
	{Sheet: 1, SrcX: 0, SrcY: 458, Width: 11, Height: 27, Flags: 0x00},
	// [57]
	{Sheet: 23, SrcX: 0, SrcY: 0, Width: 64, Height: 27, Flags: 0x00},
	// [58]
	{Sheet: 1, SrcX: 12, SrcY: 458, Width: 11, Height: 27, Flags: 0x00},
	// [59]
	{Sheet: 1, SrcX: 0, SrcY: 0, Width: 31, Height: 184, Flags: 0x00},
	// [60]
	{Sheet: 2, SrcX: 0, SrcY: 20, Width: 64, Height: 9, Flags: 0x00},
	// [61]
	{Sheet: 9, SrcX: 0, SrcY: 0, Width: 32, Height: 32, Flags: 0x00},
	// [62]
	{Sheet: 1, SrcX: 282, SrcY: 206, Width: 37, Height: 34, Flags: 0x01},
	// [63]
	{Sheet: 1, SrcX: 207, SrcY: 168, Width: 73, Height: 34, Flags: 0x00},
	// [64]
	{Sheet: 1, SrcX: 281, SrcY: 168, Width: 12, Height: 34, Flags: 0x00},
	// [65] scroll up arrow
	{Sheet: 1, SrcX: 64, SrcY: 379, Width: 17, Height: 16, Flags: 0x00},
	// [66]
	{Sheet: 1, SrcX: 84, SrcY: 379, Width: 17, Height: 16, Flags: 0x00},
	// [67] scroll down arrow
	{Sheet: 1, SrcX: 64, SrcY: 397, Width: 17, Height: 16, Flags: 0x00},
	// [68]
	{Sheet: 1, SrcX: 84, SrcY: 397, Width: 17, Height: 16, Flags: 0x00},
	// [69]
	{Sheet: 1, SrcX: 65, SrcY: 415, Width: 15, Height: 15, Flags: 0x00},
	// [70]
	{Sheet: 1, SrcX: 81, SrcY: 415, Width: 15, Height: 15, Flags: 0x00},
	// [71]
	{Sheet: 1, SrcX: 65, SrcY: 432, Width: 15, Height: 15, Flags: 0x00},
	// [72]
	{Sheet: 1, SrcX: 81, SrcY: 432, Width: 15, Height: 15, Flags: 0x00},
	// [73]
	{Sheet: 1, SrcX: 103, SrcY: 378, Width: 24, Height: 24, Flags: 0x00},
	// [74]
	{Sheet: 1, SrcX: 130, SrcY: 378, Width: 24, Height: 24, Flags: 0x00},
	// [75]
	{Sheet: 1, SrcX: 103, SrcY: 404, Width: 24, Height: 24, Flags: 0x00},
	// [76]
	{Sheet: 1, SrcX: 130, SrcY: 404, Width: 24, Height: 24, Flags: 0x00},
	// [77]
	{Sheet: 1, SrcX: 207, SrcY: 203, Width: 46, Height: 44, Flags: 0x00},
	// [78]
	{Sheet: 1, SrcX: 305, SrcY: 203, Width: 16, Height: 44, Flags: 0x00},
	// [79]
	{Sheet: 1, SrcX: 69, SrcY: 249, Width: 52, Height: 44, Flags: 0x00},
	// [80]
	{Sheet: 1, SrcX: 69, SrcY: 204, Width: 52, Height: 44, Flags: 0x00},
	// [81]
	{Sheet: 1, SrcX: 10, SrcY: 241, Width: 13, Height: 12, Flags: 0x00},
	// [82]
	{Sheet: 18, SrcX: 0, SrcY: 0, Width: 13, Height: 64, Flags: 0x00},
	// [83]
	{Sheet: 1, SrcX: 10, SrcY: 254, Width: 13, Height: 13, Flags: 0x00},
	// [84]
	{Sheet: 1, SrcX: 9, SrcY: 268, Width: 14, Height: 15, Flags: 0x00},
	// [85]
	{Sheet: 1, SrcX: 128, SrcY: 460, Width: 17, Height: 17, Flags: 0x00},
	// [86]
	{Sheet: 1, SrcX: 134, SrcY: 445, Width: 18, Height: 16, Flags: 0x00},
	// [87]
	{Sheet: 1, SrcX: 129, SrcY: 479, Width: 15, Height: 15, Flags: 0x00},
	// [88]
	{Sheet: 1, SrcX: 129, SrcY: 496, Width: 14, Height: 15, Flags: 0x00},
	// [89]
	{Sheet: 1, SrcX: 145, SrcY: 496, Width: 15, Height: 15, Flags: 0x00},
	// [90]
	{Sheet: 1, SrcX: 129, SrcY: 429, Width: 15, Height: 15, Flags: 0x00},
	// [91]
	{Sheet: 6, SrcX: 0, SrcY: 0, Width: 64, Height: 64, Flags: 0x00},
	// [92] ornate TL corner
	{Sheet: 1, SrcX: 68, SrcY: 150, Width: 26, Height: 26, Flags: 0x00},
	// [93] ornate top edge
	{Sheet: 11, SrcX: 0, SrcY: 0, Width: 64, Height: 26, Flags: 0x00},
	// [94] ornate TR corner
	{Sheet: 1, SrcX: 95, SrcY: 150, Width: 26, Height: 26, Flags: 0x00},
	// [95] ornate right edge
	{Sheet: 10, SrcX: 0, SrcY: 0, Width: 26, Height: 64, Flags: 0x00},
	// [96] ornate fill
	{Sheet: 7, SrcX: 0, SrcY: 0, Width: 64, Height: 64, Flags: 0x00},
	// [97] ornate left edge
	{Sheet: 12, SrcX: 0, SrcY: 0, Width: 26, Height: 64, Flags: 0x00},
	// [98] ornate BL corner
	{Sheet: 1, SrcX: 68, SrcY: 177, Width: 26, Height: 26, Flags: 0x00},
	// [99] ornate bottom edge
	{Sheet: 13, SrcX: 0, SrcY: 0, Width: 64, Height: 26, Flags: 0x00},
	// [100] ornate BR corner
	{Sheet: 1, SrcX: 95, SrcY: 177, Width: 26, Height: 26, Flags: 0x00},
	// [101] line TL corner
	{Sheet: 1, SrcX: 32, SrcY: 150, Width: 2, Height: 2, Flags: 0x00},
	// [102] line top edge
	{Sheet: 2, SrcX: 0, SrcY: 29, Width: 64, Height: 2, Flags: 0x00},
	// [103] line TR corner
	{Sheet: 1, SrcX: 37, SrcY: 150, Width: 2, Height: 2, Flags: 0x00},
	// [104] line right edge
	{Sheet: 3, SrcX: 29, SrcY: 0, Width: 2, Height: 64, Flags: 0x00},
	// [105] line BL corner
	{Sheet: 1, SrcX: 32, SrcY: 154, Width: 2, Height: 2, Flags: 0x00},
	// [106] line BR corner
	{Sheet: 1, SrcX: 37, SrcY: 154, Width: 2, Height: 2, Flags: 0x00},
	// [107]
	{Sheet: 1, SrcX: 180, SrcY: 288, Width: 26, Height: 26, Flags: 0x00},
	// [108]
	{Sheet: 1, SrcX: 180, SrcY: 315, Width: 26, Height: 26, Flags: 0x00},
	// [109]
	{Sheet: 1, SrcX: 37, SrcY: 0, Width: 475, Height: 107, Flags: 0x00},
	// [110]
	{Sheet: 1, SrcX: 154, SrcY: 357, Width: 14, Height: 12, Flags: 0x00},
	// [111]
	{Sheet: 1, SrcX: 170, SrcY: 357, Width: 9, Height: 12, Flags: 0x00},
	// [112]
	{Sheet: 1, SrcX: 154, SrcY: 370, Width: 14, Height: 12, Flags: 0x00},
	// [113]
	{Sheet: 1, SrcX: 169, SrcY: 371, Width: 12, Height: 12, Flags: 0x00},
	// [114]
	{Sheet: 1, SrcX: 168, SrcY: 352, Width: 11, Height: 2, Flags: 0x00},
	// [115] face hostile (red)
	{Sheet: 1, SrcX: 44, SrcY: 329, Width: 23, Height: 21, Flags: 0x00},
	// [116] face neutral (gray)
	{Sheet: 1, SrcX: 46, SrcY: 309, Width: 19, Height: 19, Flags: 0x00},
	// [117] face friendly (blue)
	{Sheet: 1, SrcX: 45, SrcY: 289, Width: 21, Height: 19, Flags: 0x00},
	// [118]
	{Sheet: 1, SrcX: 69, SrcY: 329, Width: 21, Height: 21, Flags: 0x00},
	// [119]
	{Sheet: 1, SrcX: 68, SrcY: 304, Width: 23, Height: 21, Flags: 0x00},
	// [120]
	{Sheet: 1, SrcX: 122, SrcY: 208, Width: 33, Height: 32, Flags: 0x00},
	// [121]
	{Sheet: 28, SrcX: 0, SrcY: 0, Width: 32, Height: 32, Flags: 0x00},
	// [122]
	{Sheet: 1, SrcX: 4, SrcY: 185, Width: 34, Height: 32, Flags: 0x00},
	// [123]
	{Sheet: 1, SrcX: 183, SrcY: 368, Width: 14, Height: 10, Flags: 0x00},
	// [124]
	{Sheet: 1, SrcX: 183, SrcY: 351, Width: 16, Height: 16, Flags: 0x00},
	// [125]
	{Sheet: 1, SrcX: 24, SrcY: 456, Width: 17, Height: 17, Flags: 0x00},
	// [126]
	{Sheet: 1, SrcX: 42, SrcY: 456, Width: 10, Height: 17, Flags: 0x00},
	// [127]
	{Sheet: 1, SrcX: 52, SrcY: 456, Width: 10, Height: 17, Flags: 0x00},
	// [128]
	{Sheet: 1, SrcX: 144, SrcY: 428, Width: 10, Height: 17, Flags: 0x00},
	// [129] compass rose
	{Sheet: 1, SrcX: 24, SrcY: 243, Width: 44, Height: 44, Flags: 0x00},
	// [130]
	{Sheet: 1, SrcX: 207, SrcY: 203, Width: 114, Height: 44, Flags: 0x00},
	// [131]
	{Sheet: 1, SrcX: 207, SrcY: 263, Width: 46, Height: 57, Flags: 0x00},
	// [132]
	{Sheet: 1, SrcX: 254, SrcY: 263, Width: 46, Height: 57, Flags: 0x00},
	// [133]
	{Sheet: 1, SrcX: 207, SrcY: 321, Width: 46, Height: 57, Flags: 0x00},
	// [134]
	{Sheet: 1, SrcX: 255, SrcY: 321, Width: 49, Height: 48, Flags: 0x00},
	// [135] ornate separator
	{Sheet: 1, SrcX: 208, SrcY: 248, Width: 104, Height: 15, Flags: 0x00},
	// [136] dark bar background
	{Sheet: 1, SrcX: 123, SrcY: 178, Width: 60, Height: 10, Flags: 0x00},
	// [137]
	{Sheet: 1, SrcX: 107, SrcY: 312, Width: 6, Height: 41, Flags: 0x00},
	// [138]
	{Sheet: 1, SrcX: 109, SrcY: 312, Width: 11, Height: 41, Flags: 0x00},
	// [139]
	{Sheet: 1, SrcX: 121, SrcY: 312, Width: 6, Height: 41, Flags: 0x00},
	// [140]
	{Sheet: 1, SrcX: 123, SrcY: 312, Width: 11, Height: 41, Flags: 0x00},
	// [141]
	{Sheet: 1, SrcX: 43, SrcY: 154, Width: 24, Height: 26, Flags: 0x00},
	// [142]
	{Sheet: 1, SrcX: 43, SrcY: 181, Width: 24, Height: 26, Flags: 0x00},
	// [143]
	{Sheet: 1, SrcX: 327, SrcY: 353, Width: 185, Height: 26, Flags: 0x00},
	// [144]
	{Sheet: 1, SrcX: 462, SrcY: 330, Width: 51, Height: 24, Flags: 0x00},
	// [145]
	{Sheet: 1, SrcX: 462, SrcY: 379, Width: 50, Height: 24, Flags: 0x00},
	// [146]
	{Sheet: 1, SrcX: 409, SrcY: 401, Width: 51, Height: 22, Flags: 0x00},
	// [147]
	{Sheet: 1, SrcX: 464, SrcY: 379, Width: 50, Height: 24, Flags: 0x00},
	// [148]
	{Sheet: 1, SrcX: 326, SrcY: 327, Width: 24, Height: 26, Flags: 0x00},
	// [149]
	{Sheet: 1, SrcX: 352, SrcY: 327, Width: 24, Height: 26, Flags: 0x00},
	// [150]
	{Sheet: 27, SrcX: 0, SrcY: 0, Width: 64, Height: 26, Flags: 0x00},
	// [151]
	{Sheet: 1, SrcX: 123, SrcY: 167, Width: 60, Height: 6, Flags: 0x00},
	// [152]
	{Sheet: 1, SrcX: 123, SrcY: 174, Width: 54, Height: 3, Flags: 0x00},
	// [153]
	{Sheet: 1, SrcX: 107, SrcY: 293, Width: 11, Height: 11, Flags: 0x00},
	// [154]
	{Sheet: 1, SrcX: 183, SrcY: 378, Width: 20, Height: 10, Flags: 0x00},
	// [155]
	{Sheet: 1, SrcX: 184, SrcY: 389, Width: 18, Height: 10, Flags: 0x00},
	// [156]
	{Sheet: 1, SrcX: 183, SrcY: 341, Width: 20, Height: 10, Flags: 0x00},
	// [157]
	{Sheet: 29, SrcX: 0, SrcY: 0, Width: 32, Height: 32, Flags: 0x00},
	// [158]
	{Sheet: 1, SrcX: 0, SrcY: 223, Width: 34, Height: 5, Flags: 0x00},
	// [159]
	{Sheet: 1, SrcX: 0, SrcY: 229, Width: 5, Height: 34, Flags: 0x00},
	// [160]
	{Sheet: 1, SrcX: 6, SrcY: 229, Width: 3, Height: 34, Flags: 0x00},
	// [161]
	{Sheet: 26, SrcX: 0, SrcY: 0, Width: 19, Height: 64, Flags: 0x00},
	// [162]
	{Sheet: 25, SrcX: 0, SrcY: 0, Width: 64, Height: 22, Flags: 0x00},
	// [163]
	{Sheet: 24, SrcX: 0, SrcY: 0, Width: 64, Height: 24, Flags: 0x00},
	// [164]
	{Sheet: 30, SrcX: 0, SrcY: 0, Width: 64, Height: 11, Flags: 0x00},
	// [165]
	{Sheet: 1, SrcX: 10, SrcY: 229, Width: 12, Height: 11, Flags: 0x00},
	// [166]
	{Sheet: 1, SrcX: 23, SrcY: 229, Width: 13, Height: 13, Flags: 0x00},
	// [167]
	{Sheet: 31, SrcX: 0, SrcY: 0, Width: 32, Height: 26, Flags: 0x00},
	// [168]
	{Sheet: 1, SrcX: 327, SrcY: 353, Width: 185, Height: 26, Flags: 0x01},
	// [169]
	{Sheet: 1, SrcX: 154, SrcY: 351, Width: 9, Height: 4, Flags: 0x00},
	// [170]
	{Sheet: 1, SrcX: 154, SrcY: 383, Width: 11, Height: 4, Flags: 0x00},
	// [171]
	{Sheet: 1, SrcX: 154, SrcY: 343, Width: 4, Height: 7, Flags: 0x00},
	// [172]
	{Sheet: 1, SrcX: 168, SrcY: 343, Width: 10, Height: 7, Flags: 0x00},
	// [173]
	{Sheet: 32, SrcX: 22, SrcY: 0, Width: 22, Height: 19, Flags: 0x00},
	// [174]
	{Sheet: 32, SrcX: 0, SrcY: 0, Width: 22, Height: 19, Flags: 0x00},
	// [175]
	{Sheet: 32, SrcX: 66, SrcY: 0, Width: 22, Height: 19, Flags: 0x00},
	// [176]
	{Sheet: 32, SrcX: 44, SrcY: 0, Width: 22, Height: 19, Flags: 0x00},
	// [177]
	{Sheet: 32, SrcX: 89, SrcY: 0, Width: 6, Height: 26, Flags: 0x00},
	// [178]
	{Sheet: 32, SrcX: 96, SrcY: 0, Width: 6, Height: 26, Flags: 0x00},
	// [179]
	{Sheet: 33, SrcX: 0, SrcY: 0, Width: 32, Height: 26, Flags: 0x00},
	// [180]
	{Sheet: 32, SrcX: 103, SrcY: 0, Width: 22, Height: 22, Flags: 0x00},
	// [181]
	{Sheet: 32, SrcX: 126, SrcY: 0, Width: 22, Height: 22, Flags: 0x00},
	// [182]
	{Sheet: 32, SrcX: 0, SrcY: 24, Width: 7, Height: 28, Flags: 0x00},
	// [183]
	{Sheet: 32, SrcX: 8, SrcY: 24, Width: 7, Height: 28, Flags: 0x00},
	// [184]
	{Sheet: 34, SrcX: 0, SrcY: 0, Width: 32, Height: 28, Flags: 0x00},
	// [185]
	{Sheet: 1, SrcX: 65, SrcY: 480, Width: 31, Height: 16, Flags: 0x00},
	// [186]
	{Sheet: 1, SrcX: 65, SrcY: 496, Width: 31, Height: 16, Flags: 0x00},
	// [187]
	{Sheet: 1, SrcX: 65, SrcY: 448, Width: 31, Height: 16, Flags: 0x00},
	// [188]
	{Sheet: 1, SrcX: 65, SrcY: 464, Width: 31, Height: 16, Flags: 0x00},
	// [189]
	{Sheet: 1, SrcX: 233, SrcY: 460, Width: 26, Height: 12, Flags: 0x00},
	// [190]
	{Sheet: 1, SrcX: 266, SrcY: 459, Width: 26, Height: 13, Flags: 0x00},
	// [191]
	{Sheet: 1, SrcX: 97, SrcY: 476, Width: 31, Height: 17, Flags: 0x00},
	// [192]
	{Sheet: 1, SrcX: 97, SrcY: 494, Width: 31, Height: 17, Flags: 0x00},
	// [193]
	{Sheet: 1, SrcX: 97, SrcY: 440, Width: 31, Height: 17, Flags: 0x00},
	// [194]
	{Sheet: 1, SrcX: 97, SrcY: 458, Width: 31, Height: 17, Flags: 0x00},
	// [195]
	{Sheet: 1, SrcX: 234, SrcY: 443, Width: 24, Height: 10, Flags: 0x00},
	// [196]
	{Sheet: 1, SrcX: 267, SrcY: 442, Width: 24, Height: 11, Flags: 0x00},
	// [197]
	{Sheet: 1, SrcX: 195, SrcY: 412, Width: 35, Height: 25, Flags: 0x00},
	// [198]
	{Sheet: 1, SrcX: 195, SrcY: 437, Width: 35, Height: 25, Flags: 0x00},
	// [199]
	{Sheet: 1, SrcX: 160, SrcY: 412, Width: 35, Height: 25, Flags: 0x00},
	// [200]
	{Sheet: 1, SrcX: 160, SrcY: 437, Width: 35, Height: 25, Flags: 0x00},
	// [201]
	{Sheet: 1, SrcX: 160, SrcY: 462, Width: 61, Height: 25, Flags: 0x00},
	// [202]
	{Sheet: 1, SrcX: 160, SrcY: 487, Width: 61, Height: 25, Flags: 0x00},
	// [203]
	{Sheet: 1, SrcX: 122, SrcY: 151, Width: 180, Height: 16, Flags: 0x00},
	// [204]
	{Sheet: 1, SrcX: 153, SrcY: 315, Width: 26, Height: 26, Flags: 0x00},
	// [205]
	{Sheet: 1, SrcX: 160, SrcY: 437, Width: 35, Height: 25, Flags: 0x00},
	// [206]
	{Sheet: 1, SrcX: 121, SrcY: 251, Width: 35, Height: 40, Flags: 0x00},
	// [207]
	{Sheet: 1, SrcX: 158, SrcY: 263, Width: 17, Height: 17, Flags: 0x00},
	// [208]
	{Sheet: 1, SrcX: 162, SrcY: 213, Width: 43, Height: 33, Flags: 0x00},
	// [209]
	{Sheet: 1, SrcX: 177, SrcY: 254, Width: 29, Height: 26, Flags: 0x00},
	// [210]
	{Sheet: 1, SrcX: 232, SrcY: 412, Width: 85, Height: 46, Flags: 0x00},
	// [211]
	{Sheet: 1, SrcX: 155, SrcY: 293, Width: 21, Height: 18, Flags: 0x00},
	// [212]
	{Sheet: 1, SrcX: 265, SrcY: 422, Width: 16, Height: 16, Flags: 0x00},
	// [213]
	{Sheet: 1, SrcX: 282, SrcY: 422, Width: 16, Height: 16, Flags: 0x00},
	// [214]
	{Sheet: 1, SrcX: 321, SrcY: 441, Width: 31, Height: 16, Flags: 0x00},
	// [215]
	{Sheet: 1, SrcX: 321, SrcY: 458, Width: 31, Height: 16, Flags: 0x00},
	// [216]
	{Sheet: 1, SrcX: 232, SrcY: 423, Width: 14, Height: 14, Flags: 0x00},
	// [217]
	{Sheet: 1, SrcX: 249, SrcY: 423, Width: 14, Height: 14, Flags: 0x00},
	// [218]
	{Sheet: 1, SrcX: 321, SrcY: 475, Width: 31, Height: 17, Flags: 0x00},
	// [219]
	{Sheet: 1, SrcX: 321, SrcY: 494, Width: 31, Height: 17, Flags: 0x00},
	// [220]
	{Sheet: 1, SrcX: 209, SrcY: 393, Width: 19, Height: 18, Flags: 0x00},
	// [221]
	{Sheet: 1, SrcX: 230, SrcY: 393, Width: 21, Height: 18, Flags: 0x00},
	// [222]
	{Sheet: 1, SrcX: 253, SrcY: 395, Width: 19, Height: 16, Flags: 0x00},
	// [223]
	{Sheet: 1, SrcX: 160, SrcY: 395, Width: 19, Height: 16, Flags: 0x00},
	// [224]
	{Sheet: 1, SrcX: 317, SrcY: 423, Width: 16, Height: 16, Flags: 0x00},
	// [225]
	{Sheet: 1, SrcX: 300, SrcY: 423, Width: 16, Height: 16, Flags: 0x00},
	// [226]
	{Sheet: 1, SrcX: 169, SrcY: 382, Width: 12, Height: 12, Flags: 0x00},
	// [227]
	{Sheet: 1, SrcX: 273, SrcY: 385, Width: 8, Height: 26, Flags: 0x00},
	// [228]
	{Sheet: 1, SrcX: 282, SrcY: 385, Width: 16, Height: 26, Flags: 0x00},
	// [229]
	{Sheet: 1, SrcX: 299, SrcY: 385, Width: 8, Height: 26, Flags: 0x00},
	// [230]
	{Sheet: 1, SrcX: 308, SrcY: 385, Width: 8, Height: 26, Flags: 0x00},
	// [231]
	{Sheet: 1, SrcX: 317, SrcY: 385, Width: 16, Height: 26, Flags: 0x00},
	// [232]
	{Sheet: 1, SrcX: 334, SrcY: 385, Width: 8, Height: 26, Flags: 0x00},
	// [233]
	{Sheet: 1, SrcX: 340, SrcY: 335, Width: 39, Height: 10, Flags: 0x00},
	// [234]
	{Sheet: 1, SrcX: 341, SrcY: 349, Width: 39, Height: 9, Flags: 0x00},
	// [235]
	{Sheet: 1, SrcX: 342, SrcY: 361, Width: 38, Height: 10, Flags: 0x00},
	// [236]
	{Sheet: 1, SrcX: 343, SrcY: 380, Width: 27, Height: 13, Flags: 0x00},
	// [237]
	{Sheet: 1, SrcX: 343, SrcY: 398, Width: 14, Height: 13, Flags: 0x00},
	// [238]
	{Sheet: 1, SrcX: 359, SrcY: 398, Width: 14, Height: 13, Flags: 0x00},
	// [239] spell slot
	{Sheet: 1, SrcX: 377, SrcY: 385, Width: 25, Height: 25, Flags: 0x00},
	// [240]
	{Sheet: 1, SrcX: 383, SrcY: 335, Width: 13, Height: 15, Flags: 0x00},
	// [241]
	{Sheet: 1, SrcX: 400, SrcY: 338, Width: 22, Height: 10, Flags: 0x00},
	// [242]
	{Sheet: 1, SrcX: 400, SrcY: 354, Width: 20, Height: 12, Flags: 0x00},
	// [243]
	{Sheet: 35, SrcX: 87, SrcY: 0, Width: 346, Height: 64, Flags: 0x00},
	// [244]
	{Sheet: 1, SrcX: 353, SrcY: 441, Width: 44, Height: 44, Flags: 0x00},
	// [245]
	{Sheet: 1, SrcX: 397, SrcY: 441, Width: 44, Height: 44, Flags: 0x00},
	// [246]
	{Sheet: 1, SrcX: 442, SrcY: 441, Width: 44, Height: 44, Flags: 0x00},
	// [247]
	{Sheet: 36, SrcX: 0, SrcY: 0, Width: 256, Height: 17, Flags: 0x00},
	// [248]
	{Sheet: 36, SrcX: 0, SrcY: 0, Width: 256, Height: 17, Flags: 0x00},
	// [249]
	{Sheet: 36, SrcX: 0, SrcY: 17, Width: 256, Height: 17, Flags: 0x00},
	// [250]
	{Sheet: 36, SrcX: 0, SrcY: 34, Width: 256, Height: 14, Flags: 0x00},
	// [251]
	{Sheet: 1, SrcX: 428, SrcY: 326, Width: 83, Height: 43, Flags: 0x00},
}
