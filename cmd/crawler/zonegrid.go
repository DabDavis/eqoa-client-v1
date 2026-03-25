package main

// Tunaria zone grid from PS2 CLIENT data section at 0x00CF3820.
// 12 columns × 15 rows, each cell = 2000×2000 world units.
// Origin: column 0 = X ~3000, row 0 = Z ~1000.
// Sentinel: 641 = ocean, 1047 = mountain/void, 0 = invalid.
// Land zones have IDs in range 357-1188.

const (
	zoneGridCols   = 12
	zoneGridRows   = 15
	zoneCellSize   = 2000.0
	zoneGridOrigX  = 3000.0  // X coordinate of column 0 center
	zoneGridOrigZ  = 1000.0  // Z coordinate of row 0 center
	zoneOcean      = 641
	zoneMountain   = 1047
)

// tunariaGrid[row][col] = zone ID. 641=ocean, 1047=mountain, 0=void.
var tunariaGrid = [zoneGridRows][zoneGridCols]uint16{
	{641, 641, 641, 641, 641, 641, 641, 641, 641, 1152, 1153, 1176},
	{1051, 1046, 1057, 1058, 1074, 1075, 364, 1097, 1123, 1124, 1151, 1154},
	{1175, 1042, 1047, 1056, 1059, 1073, 1076, 1096, 1098, 1122, 1125, 1150},
	{361, 1174, 1045, 1047, 1055, 1060, 1072, 1077, 1095, 608, 1121, 1126},
	{1149, 1155, 1173, 1047, 1047, 1054, 1061, 1071, 1078, 1094, 1099, 1120},
	{1127, 1148, 1156, 1172, 1047, 1047, 1053, 1062, 602, 1079, 1093, 1100},
	{1119, 1128, 1147, 1157, 1171, 1047, 1047, 1052, 1063, 1070, 1080, 1092},
	{1101, 1118, 1129, 1146, 1158, 358, 1047, 1047, 357, 1064, 1069, 1081},
	{1091, 1102, 1117, 1130, 1145, 1159, 1170, 1047, 1047, 1050, 1065, 1068},
	{1082, 1090, 1103, 1116, 1131, 1144, 1160, 1047, 1047, 1048, 366, 1066},
	{1067, 1083, 1089, 1104, 1115, 1132, 1143, 1161, 1047, 1047, 1049, 1049},
	{641, 641, 641, 1088, 1105, 1114, 1133, 1142, 1162, 1047, 1047, 641},
	{641, 641, 641, 641, 1087, 1106, 1113, 1134, 1141, 1163, 1169, 1047},
	{641, 641, 641, 641, 641, 1086, 1107, 1112, 1135, 1140, 1164, 1168},
	{1047, 641, 641, 641, 641, 641, 1085, 1108, 1111, 1136, 1139, 1165},
}

// isLandZone returns true if the world position (x, z) is over a land zone.
func isLandZone(x, z float32) bool {
	col := int((x - zoneGridOrigX + zoneCellSize/2) / zoneCellSize)
	row := int((z - zoneGridOrigZ + zoneCellSize/2) / zoneCellSize)
	if col < 0 || col >= zoneGridCols || row < 0 || row >= zoneGridRows {
		return false
	}
	zid := tunariaGrid[row][col]
	return zid != 0 && zid != zoneOcean && zid != zoneMountain
}

// landColumnRanges returns the min/max Z values that are land for a given X.
// Used to skip ocean/mountain at the start and end of each column sweep.
func landColumnRanges(x float32) (minZ, maxZ float32, hasLand bool) {
	col := int((x - zoneGridOrigX + zoneCellSize/2) / zoneCellSize)
	if col < 0 || col >= zoneGridCols {
		return 0, 0, false
	}

	first := -1
	last := -1
	for row := 0; row < zoneGridRows; row++ {
		zid := tunariaGrid[row][col]
		if zid != 0 && zid != zoneOcean && zid != zoneMountain {
			if first < 0 {
				first = row
			}
			last = row
		}
	}
	if first < 0 {
		return 0, 0, false
	}

	// Convert row indices to world Z coordinates
	minZ = zoneGridOrigZ + float32(first)*zoneCellSize - zoneCellSize/2
	maxZ = zoneGridOrigZ + float32(last)*zoneCellSize + zoneCellSize/2
	return minZ, maxZ, true
}
