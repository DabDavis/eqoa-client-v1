package game

import "fmt"

// The world table and its zone grids, as CLIENT holds them: a world's name is
// a string id, and a zone is a cell of the world's own grid, whose value is
// the zone's string id (CLIENT+0x10FC88, 0x00CC4608: (world, row, col) →
// id, 641 "Empty" off the grid). The grids were read out of the client's data
// in an in-world EE dump (0x00CF3820 …); both bounds of each are inclusive in
// the world table (0x00CF3738), so Tunaria is 13 columns by 16 rows.
//
// Sandstorm's S2 extends the table to 8 worlds (6 Underfoot, 7 Secrets again)
// but not the grid switch, so 6 and 7 have no zones.

// ZoneEmpty is string 641, "Empty": the lookup's answer off a grid.
const ZoneEmpty = 641

// WorldNameIDs are the worlds' name string ids (0x413 "Tunaria" …). 5-7
// are all "Secret Zones".
var WorldNameIDs = [8]int{0x413, 0x414, 0x5A0, 0x5B6, 0x5B7, 0x6F8, 0x6F8, 0x6F8}

// zoneGrids[world][row][col] are the zone string ids.
var zoneGrids = [6][][]uint16{
	{ // 0 Tunaria, 13 × 16, 0x00CF3820
		{641, 641, 641, 641, 641, 641, 641, 641, 641, 1152, 1153, 1176, 1051},
		{1046, 1057, 1058, 1074, 1075, 364, 1097, 1123, 1124, 1151, 1154, 1175, 1042},
		{1047, 1056, 1059, 1073, 1076, 1096, 1098, 1122, 1125, 1150, 361, 1174, 1045},
		{1047, 1055, 1060, 1072, 1077, 1095, 608, 1121, 1126, 1149, 1155, 1173, 1047},
		{1047, 1054, 1061, 1071, 1078, 1094, 1099, 1120, 1127, 1148, 1156, 1172, 1047},
		{1047, 1053, 1062, 602, 1079, 1093, 1100, 1119, 1128, 1147, 1157, 1171, 1047},
		{1047, 1052, 1063, 1070, 1080, 1092, 1101, 1118, 1129, 1146, 1158, 358, 1047},
		{1047, 357, 1064, 1069, 1081, 1091, 1102, 1117, 1130, 1145, 1159, 1170, 1047},
		{1047, 1050, 1065, 1068, 1082, 1090, 1103, 1116, 1131, 1144, 1160, 1047, 1047},
		{1048, 366, 1066, 1067, 1083, 1089, 1104, 1115, 1132, 1143, 1161, 1047, 1047},
		{1049, 1049, 641, 641, 641, 1088, 1105, 1114, 1133, 1142, 1162, 1047, 1047},
		{641, 641, 641, 641, 641, 1087, 1106, 1113, 1134, 1141, 1163, 1169, 1047},
		{641, 641, 641, 641, 641, 1086, 1107, 1112, 1135, 1140, 1164, 1168, 1047},
		{641, 641, 641, 641, 641, 1085, 1108, 1111, 1136, 1139, 1165, 1167, 1047},
		{641, 641, 641, 641, 641, 1084, 1109, 1110, 1137, 1138, 1166, 363, 1047},
		{641, 641, 641, 641, 641, 1047, 1047, 1047, 1047, 1047, 1047, 1047, 1047},
	},
	{ // 1 Rathe Mountains, 4 × 4, 0x00CF39C0
		{641, 1177, 1181, 1185},
		{641, 1178, 1182, 1186},
		{641, 1179, 1183, 1187},
		{641, 1180, 1184, 1188},
	},
	{ // 2 Odus, 6 × 7, 0x00CF39E0
		{641, 1441, 1451, 1446, 1456, 641},
		{641, 1442, 1447, 1452, 1457, 641},
		{641, 1443, 1448, 1453, 1458, 641},
		{641, 1444, 1449, 1454, 1459, 641},
		{641, 1445, 1450, 1455, 1460, 641},
		{641, 641, 641, 641, 641, 641},
		{641, 641, 641, 641, 641, 641},
	},
	{ // 3 Lavastorm Mountains, 3 × 2, 0x00CF3A38
		{641, 641, 641},
		{641, 1461, 1462},
	},
	{ // 4 Plane of Sky, 2 × 3, 0x00CF3A48
		{641, 641},
		{641, 1463},
		{641, 1463},
	},
	{ // 5 Secret Zones, 2 × 3, 0x00CF3A58
		{641, 1785},
		{641, 641},
		{641, 1786},
	},
}

// ZoneAt is CLIENT's (world, row, col) → zone string id: ZoneEmpty off the
// grid or for a world without one.
func ZoneAt(world, col, row int) int {
	if world < 0 || world >= len(zoneGrids) {
		return ZoneEmpty
	}
	g := zoneGrids[world]
	if row < 0 || row >= len(g) || col < 0 || col >= len(g[row]) {
		return ZoneEmpty
	}
	return int(g[row][col])
}

// LocationLine is the player search's "Location:" line (S2 cave 0x000FBB80):
// the world's name, and the zone's when the grid names one (not 0, not 641).
// world must be one of the 8; an unknown string reads as its id.
func LocationLine(w Words, world, col, row int) string {
	if world < 0 || world >= len(WorldNameIDs) {
		return ""
	}
	name := func(id int) string {
		if s := w.Get(id); s != "" {
			return s
		}
		return fmt.Sprintf("#%d", id)
	}
	worldName := name(WorldNameIDs[world])
	if z := ZoneAt(world, col, row); z != 0 && z != ZoneEmpty {
		return fmt.Sprintf("Location: %s, %s", worldName, name(z))
	}
	return "Location: " + worldName
}
