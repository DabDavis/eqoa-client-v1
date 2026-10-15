package models

// ArmorTextures maps [armorSet][textSlot] to ESF Surface DictIDs.
// TextSlots: 0=chest, 1=bracer, 2=gloves, 3=legs, 4=boots
// Source: PS2 VICSpriteCust::$Armor table at EE 0x004FB910 stores VIRaster pool
// indices. DictIDs resolved from runtime VIRaster pool in EE memory dump
// (pool at 0x0076B5A0, VISurface+0x00 = DictID).
// Sets 0-8 are the PS2 sets. Sets 9-10 are client-side only — see
// ArmorSetOrnatePlate and ArmorSetTemplarPlate.
var ArmorTextures = [ArmorSetCount][5]int32{
	{0, 0, 0, 0, 0},                                                                     // 0 = none (base model texture)
	{-1085571574, 1648643354, -2066550155, 750072456, 2060964273},                        // 1 = cloth
	{1926263787, -1180968619, 945285206, -1489171117, -178279300},                         // 2 = leather
	{1477461748, -1058253624, 496483167, 113917398, 1424809215},                           // 3 = chainmail
	{1085332903, -348034519, 104354322, -184150041, 1126741776},                           // 4 = plate
	{968685663, -435228783, -12292918, 1978836175, -1005239304},                           // 5 = padded
	{-112471789, -1599977331, -1093450370, 1872225035, -1111850444},                       // 6 = banded mail
	{637778783, 596508817, -343199798, -1925225009, -614333192},                           // 7 = scale
	{1071320539, -2127282779, 90341958, 241961123, 1552852940},                            // 8 = monk
	{1085332903, -348034519, 104354322, -184150041, 1126741776},                           // 9 = ornate plate (same IDs as 4, CHAR.ESF art)
	{-1326051557, -718269054, -718269054, -718269054, -718269054},                         // 10 = templar plate (CHAR.ESF model 0xD1300BD3)
}

// ArmorSetCount is the number of selectable armor sets, including client-side ones.
const ArmorSetCount = 11

// ArmorSetOrnatePlate is a cosmetic set with no PS2 $Armor entry. It reuses the
// plate DictIDs but resolves them from CHAR.ESF instead of CHARCUST.CSF.
//
// Both files store surfaces under these five DictIDs, with different art:
// CHARCUST.CSF holds the plain steel plate the retail client shows, while
// CHAR.ESF holds an ornate gold-and-steel variant that is never displayed,
// because the texture lookup searches CHARCUST first and stops on the first hit.
// Verified by exact RGB pixel match — see go-eqoa-esf-tools-v2/cmd/matchtex and
// extracted-assets/gold-silver-armor/README.md.
const ArmorSetOrnatePlate byte = 9

// ArmorSetTemplarPlate is a second cosmetic set with no PS2 $Armor entry,
// adapted from the armor worn by CHAR.ESF model 0xD1300BD3 (-785380397): silver
// plate with a blue tabard and gold shoulder medallions.
//
// Unlike the ornate set, these DictIDs are unique to CHAR.ESF — CHARCUST.CSF has
// no surface under either, so nothing shadows them. That model packs its armor
// into just two atlases, which is why four slots share one DictID: the donor
// lays its limb art out across the legs, boots, bracer and gloves UV tiles of a
// single 128x128 texture, exactly the tiles the player body samples for those
// slots. Chest gets its own 128x128.
//
// Derived and measured with `npctex -fit -ref 1893243078 -dict -785380397`,
// which matches an untagged donor's materials to the player body's per-slot UV
// footprints. That model carries no CSpriteTSlotList, so it is invisible to
// `-compat`/`-matrix` and never appeared in the 63-pair NPC survey. Per slot the
// winning texture is unopposed (no other material has any coverage of the tile),
// and the art covers 0.97/0.71/0.91/0.96/0.91 of the footprint the player
// actually samples for chest/bracer/gloves/legs/boots.
//
// The donor's face (1379008418) and hair (1842279120) are deliberately NOT
// carried: an armor set is only the five $Armor text slots, so the wearer keeps
// their own head. See claude-notes/npc-armor-texture-survey.md.
const ArmorSetTemplarPlate byte = 10

// armorSetFromCharESF marks sets whose textures must be read from CHAR.ESF
// rather than through the default CHARCUST-first search order.
var armorSetFromCharESF = [ArmorSetCount]bool{
	ArmorSetOrnatePlate:  true,
	ArmorSetTemplarPlate: true,
}

// ArmorSetNames labels each armor set for UI.
var ArmorSetNames = [ArmorSetCount]string{
	"None", "Cloth", "Leather", "Chainmail", "Plate",
	"Padded", "Banded Mail", "Scale", "Monk", "Ornate Plate",
	"Templar Plate",
}

// TextSlot indices for ArmorTextures second dimension.
const (
	TextSlotChest  = 0
	TextSlotBracer = 1
	TextSlotGloves = 2
	TextSlotLegs   = 3
	TextSlotBoots  = 4
)

// TintColor represents an RGBA tint color from the PS2 VICSpriteCust::$Tints table.
type TintColor struct {
	R, G, B, A uint8
}

// Tints maps tint index to RGBA color: VICSpriteCust::Tints[15]. Index 14 is
// yellow; with only 14 entries it fell through to white. SetArmorSlot turns
// any index of 15 or more into 0, as GetTintColor does.
var Tints = [15]TintColor{
	{0xFF, 0xFF, 0xFF, 0xFF}, // 0 = White
	{0x5A, 0x5A, 0x5A, 0xFF}, // 1 = DarkGray
	{0x00, 0xB4, 0xFF, 0xFF}, // 2 = Cyan
	{0x96, 0x64, 0x32, 0xFF}, // 3 = Brown
	{0x00, 0x00, 0x8C, 0xFF}, // 4 = Navy
	{0x00, 0xC8, 0x32, 0xFF}, // 5 = Green
	{0xFA, 0xAF, 0x32, 0xFF}, // 6 = Gold
	{0x50, 0x64, 0x00, 0xFF}, // 7 = Olive
	{0xF0, 0x3C, 0x3C, 0xFF}, // 8 = Red
	{0xA5, 0xF5, 0xFF, 0xFF}, // 9 = IceBlue
	{0xA0, 0xAA, 0xB4, 0xFF}, // 10 = Silver
	{0xDC, 0xBE, 0x96, 0xFF}, // 11 = Tan
	{0x00, 0xC8, 0xB4, 0xFF}, // 12 = Teal
	{0x96, 0x00, 0x3C, 0xFF}, // 13 = Purple
	{0xF0, 0xF0, 0x00, 0xFF}, // 14 = Yellow
}

// TintNames labels each entry of Tints for UI.
var TintNames = [len(Tints)]string{
	"White", "Dark Gray", "Cyan", "Brown", "Navy", "Green", "Gold",
	"Olive", "Red", "Ice Blue", "Silver", "Tan", "Teal", "Purple", "Yellow",
}

// RobeTextures maps [robeStyle] to ESF Surface DictIDs.
// Each robe has 2 textures: [0]=chest, [1]=legs.
// Source: PS2 $Robe table at 0x004FBB48, pool indices resolved from EE dump.
var RobeTextures = [4][2]int32{
	{1163857248, 2111470074},    // robe 0: pool[199,200]
	{-1466178805, 222590899},    // robe 1: pool[201,202]
	{1563400161, 1163725733},    // robe 2: pool[203,204]
	{656438062, -1236576364},    // robe 3: pool[205,206]
}

// HelmTextures maps helm index to ESF Surface DictID in CHARCUST.CSF.
// Source: The 8 unidentified 64x64 surfaces in CHARCUST.CSF, matched to
// PCSX2-captured helm textures via pixel comparison (error=0.00 for helms 1-6).
// Helms 7-8 are the remaining 2 surfaces (assignment TBD — user verification needed).
var HelmTextures = [9]int32{
	0,            // helm 0 = none
	727259660,    // helm 1: cloth
	1387357875,   // helm 2: leather
	-1389497233,  // helm 3: scale
	-549814268,   // helm 4: chain
	-1194253819,  // helm 5: banded
	-941943113,   // helm 6: plate
	-113096367,   // helm 7: scale armor set — swapped with 8 per user verification
	1806117984,   // helm 8: monk armor set — swapped with 7 per user verification
}

// HairTextures maps hair style to ESF Surface DictID.
// Source: PS2 $Hair table at 0x004FBB28, pool indices resolved from EE dump.
var HairTextures = [8]int32{
	-1728551799,  // hair 0: pool[190]
	572420760,    // hair 1: pool[191]
	1842279120,   // hair 2: pool[192]
	-21175083,    // hair 3: pool[193]
	-728151733,   // hair 4: pool[194]
	-1993224474,  // hair 5: pool[195]
	-1618491473,  // hair 6: pool[196]
	-596002877,   // hair 7: pool[197]
}

// HairNullTexture is the bald/no-hair ESF Surface DictID.
var HairNullTexture int32 = 1125869994 // pool[198]

// FaceTextures maps [row][faceIndex*2+sex] to ESF Surface hash DictID, mirroring
// the PS2 $Race table's layout (Race[race*8 + face*2 + sex], sex 0=male).
// The 10 rows x 8 entries account for CHARFACE.CSF exactly: all 80 surfaces, no
// spares. Ogre is the row absent from the 72-entry PS2 $Race table.
//
// Rows are ordered by the NETWORK race enum (HUM=0 … OGR=9), which is *not*
// CSprite.Race at the top end — index with FaceRowForCSpriteRace, never with a
// CSprite Race value directly. See that function for the divergence.
//
// The row names below were wrong for rows 4-7 (they read Halfling/Dwarf/Troll/
// Barbarian). Corrected by matching each row's face art to the body skin it has
// to sit next to: normalised body-vs-face RGB agrees to within a percent on every
// row, and row 5 is unmistakable — both its face and its body skin are green
// (35,38,27 with G > R), i.e. Troll, which the network enum also puts at 5.
var FaceTextures = [10][8]int32{
	{1249555534, 1985805693, 1544055455, -2014661682, 1838555376, -1720161761, 2133055297, -1425661840},   // 0 Human
	{1953705756, -130205509, -2046761619, 164294412, -1752261698, 458794333, -1457761777, 753294254},      // 1 Elf
	{1123782610, -329760479, 1418282531, -35260558, 1712782452, 259239363, 2007282373, 553739284},         // 2 DarkElf  (blue-grey)
	{-1716470973, 1662343962, -1421971052, 1956843883, -1127471131, -2043623492, -832971210, -1749123571}, // 3 Gnome
	{1870557257, -1112454576, -2129910118, -817954655, -1835410197, -523454734, -1540910276, -228954813},  // 4 Dwarf
	{313034802, 2121145473, 607534723, -1879321902, 902034644, -1584821981, 1196534565, -1290322060},      // 5 Troll     (green)
	{-638844159, 686922440, -344344238, 981422361, -49844317, 1275922282, 244655604, 1570422203},          // 6 Barbarian
	{1837018216, -1150319513, 2131518137, -855819592, -1868949238, -561319671, -1574449317, -266819750},   // 7 Halfling
	{-735732927, 131118344, -441233006, 425618265, -146733085, 720118186, 147766836, 1014618107},          // 8 Erudite  (dark)
	{-50023566, -620928575, 244476355, -326428654, 538976276, -31928733, 833476197, 262571188},            // 9 Ogre
}

// FaceRowNames labels each FaceTextures row for UI and tooling.
var FaceRowNames = [len(FaceTextures)]string{
	"Human", "Elf", "DarkElf", "Gnome", "Dwarf",
	"Troll", "Barbarian", "Halfling", "Erudite", "Ogre",
}

// FaceRowForCSpriteRace maps a model's CSprite.Race to a FaceTextures row, or
// -1 when the model has no face row.
//
// The two enums agree for 0-8 but diverge above that:
//
//   - CSprite.Race 9 is not Ogre, it is the parser's "unset" default. pkg/esf
//     sets Race = 9 when the CSpriteHeader predates the field, so every creature
//     and prop in CHAR.ESF reports 9. No race-9 model is a player body: none has
//     the two LOD sub-sprites and >3000 verts every real body has.
//   - The Ogre player body reports CSprite.Race 10, which the network enum has no
//     value for and which therefore has no row of its own.
//
// Indexing FaceTextures with CSprite.Race directly gives Ogres no face at all
// (row 10 is out of range) and would hand creature models the Ogre faces.
func FaceRowForCSpriteRace(cspriteRace byte) int {
	switch {
	case cspriteRace <= 8:
		return int(cspriteRace)
	case cspriteRace == 10:
		return 9 // Ogre
	default:
		return -1 // 9 = unset sentinel, or out of range
	}
}

// GetArmorTextureDictID returns the ESF Surface DictID for an armor texture.
// Returns 0 if the armor set is 0 (no armor) or out of range.
//
// Callers that load the texture should prefer GetArmorTexture, which also
// reports which ESF file the DictID must be read from.
func GetArmorTextureDictID(armorSet byte, textSlot int) int32 {
	dictID, _ := GetArmorTexture(armorSet, textSlot)
	return dictID
}

// GetArmorTexture returns the ESF Surface DictID for an armor texture and
// whether it must be read from CHAR.ESF rather than through the default
// CHARCUST-first search order. Returns (0, false) for set 0 or out of range.
func GetArmorTexture(armorSet byte, textSlot int) (dictID int32, fromCharESF bool) {
	if int(armorSet) >= len(ArmorTextures) || textSlot < 0 || textSlot >= 5 {
		return 0, false
	}
	return ArmorTextures[armorSet][textSlot], armorSetFromCharESF[armorSet]
}

// GetRobeTextureDictID returns the ESF Surface DictID for a robe texture.
// textSlot 0 (chest) → base robe texture, textSlot 3 (legs) → overlay.
// Returns 0 for other slots or invalid robe index.
func GetRobeTextureDictID(robeIndex byte, textSlot int) int32 {
	if int(robeIndex) >= len(RobeTextures) {
		return 0
	}
	switch textSlot {
	case TextSlotChest:
		return RobeTextures[robeIndex][0]
	case TextSlotLegs:
		return RobeTextures[robeIndex][1]
	default:
		return 0
	}
}

// GetHairTextureDictID returns the ESF Surface DictID for a hair texture.
// SetHair reads Hair[hair < 8 ? hair : 7]: an index past the table is the
// last colour, not bald.
func GetHairTextureDictID(hairStyle byte) int32 {
	if int(hairStyle) >= len(HairTextures) {
		return HairTextures[len(HairTextures)-1]
	}
	return HairTextures[hairStyle]
}

// HairColorRGBA maps HairColor index to an average RGB color sampled from each
// HairTexture. Used to tint gnome mustache meshes (which have null-texture UVs
// and white vertex colors — need a solid-color texture to match the beard).
var HairColorRGBA = [8][4]byte{
	{67, 67, 61, 255},     // 0: dark gray/charcoal
	{97, 69, 42, 255},     // 1: brown
	{190, 170, 116, 255},  // 2: blonde/sandy
	{155, 155, 155, 255},  // 3: silver/gray
	{151, 62, 25, 255},    // 4: auburn/red
	{94, 132, 59, 255},    // 5: green
	{107, 155, 142, 255},  // 6: teal
	{143, 92, 136, 255},   // 7: purple/mauve
}

// GetHairColor returns the RGB tint color for a given HairColor index.
func GetHairColor(hairColor byte) (r, g, b byte) {
	if int(hairColor) >= len(HairColorRGBA) {
		return 67, 67, 61 // fallback to [0]
	}
	c := HairColorRGBA[hairColor]
	return c[0], c[1], c[2]
}

// GetHelmTextureDictID returns the ESF Surface DictID for a helm texture.
// Returns 0 if helm index is 0 (none) or out of range.
func GetHelmTextureDictID(helmIndex byte) int32 {
	if int(helmIndex) >= len(HelmTextures) {
		return 0
	}
	return HelmTextures[helmIndex]
}

// HelmModelDictIDs maps helm index to the SimpleSprite DictID for the 3D helm model
// in CHARCUST.CSF. Each helm is a standalone SimpleSprite with its own geometry,
// material palette, and collision buffer. The PS2 uses VIScene::CopySprite +
// VIHSprite::Attach to parent these models to the character hierarchy.
// Helms 1-4: verified from $Helm pool indices (pools 28-31 were valid at dump).
// Helms 5-8: pool data was stale; corrected by visual identification in-game.
//   - Model 1515302081 = padded (user confirmed at helm 5)
//   - Model 505782879 = scale (original $Helm[7]=pool 34 mapping)
//   - Models 1904536637/62448723 assigned to 6/8 by vertex complexity
var HelmModelDictIDs = [9]int32{
	0,            // helm 0 = none
	208613338,    // helm 1: cloth       (282 verts)
	856507303,    // helm 2: leather     (256 verts)
	127839378,    // helm 3: chainmail   (412 verts) — user confirmed
	62448723,     // helm 4: plate       (180 verts) — user confirmed
	1515302081,   // helm 5: padded      (256 verts) — user confirmed
	1904536637,   // helm 6: banded      (280 verts)
	-1847324229,  // helm 7: scale       (402 verts) — swapped with chain
	505782879,    // helm 8: monk        (332 verts) — user confirmed
}

// GetHelmModelDictID returns the SimpleSprite DictID for a helm 3D model.
// Returns 0 if helm index is 0 (none) or out of range.
func GetHelmModelDictID(helmIndex byte) int32 {
	if int(helmIndex) >= len(HelmModelDictIDs) {
		return 0
	}
	return HelmModelDictIDs[helmIndex]
}

// GetTintColor returns the RGBA tint color for a tint index.
// Returns white (index 0) if out of range.
func GetTintColor(tintIndex byte) TintColor {
	if int(tintIndex) >= len(Tints) {
		return Tints[0]
	}
	return Tints[tintIndex]
}

// GetFaceTexture returns the ESF hash DictID for a face texture.
//
// cspriteRace is a model's CSprite.Race — the value the renderer has, from
// VICSprite::GetRace — not a network race byte. The row is resolved through
// FaceRowForCSpriteRace, which is what gets Ogres (CSprite.Race 10) their faces
// and keeps creature models (the unset sentinel 9) from borrowing them.
//
// Returns 0 when the model has no face row or the index is out of range.
func GetFaceTexture(cspriteRace, faceOption, sex byte) int32 {
	row := FaceRowForCSpriteRace(cspriteRace)
	if row < 0 || int(faceOption) >= 4 {
		return 0
	}
	idx := int(faceOption)*2 + int(sex)
	if idx >= 8 {
		return 0
	}
	return FaceTextures[row][idx]
}

// isHairDictID returns true if the given DictID matches any known hair texture.
func isHairDictID(dictID int32) bool {
	if dictID == HairNullTexture {
		return true
	}
	for _, h := range HairTextures {
		if dictID == h {
			return true
		}
	}
	return false
}

// isFaceDictID returns true if the given DictID matches any known face texture.
func isFaceDictID(dictID int32) bool {
	for r := range FaceTextures {
		for _, f := range FaceTextures[r] {
			if dictID == f {
				return true
			}
		}
	}
	return false
}
