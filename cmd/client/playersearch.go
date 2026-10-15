package main

import (
	"fmt"
	"sort"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/game"
)

// Sandstorm's player search, from its S2 patch
// (claude-notes/sandstorm-s2-live-20261015.md): the Group menu's Search,
// Register and Unregister, here in the Social menu. Search sends 0x1502 with
// the filters; the server answers 0x1501 (clear) and one 0x1500 per player.
// Tell starts a "/t <name> " line (S2 cave 0x000FAA40). Invite is the retail
// group invite, 0x0625 with the entry's id and name (cave 0x000FAB10 calls
// CLIENT sub_0237A8, the Group menu's own invite).

// The search window's short names (S2 tables 0x000FE780, 0x000FE7C0): the low
// nibble indexes them, and anything past the end prints "?".
var searchRaceShort = []string{"HUM", "ELF", "DELF", "GNO", "DWF", "TRL", "BAR", "HLF", "ERU", "OGR"}
var searchClassShort = []string{"WAR", "RAN", "PAL", "SK", "MNK", "BRD", "RGE", "DRD", "SHA", "CL", "MAG", "NEC", "ENC", "WIZ", "ALC"}

func searchShort(names []string, b byte) string {
	if i := int(b & 0x0F); i < len(names) {
		return names[i]
	}
	return "?"
}

// searchGroupLine is the window's "Group:" line (cave 0x000FBEA0): the byte
// is the group size, 0 not grouped, and the note follows it. A size of 5 or
// more — the 0xFF a short entry defaults to included — shows no line.
func searchGroupLine(e game.PlayerSearchEntry) string {
	var g string
	switch {
	case e.Group == 0:
		g = "Group: no"
	case e.Group <= 4:
		g = fmt.Sprintf("Group: %d", e.Group)
	default:
		return ""
	}
	if e.Note != "" {
		g += " - " + e.Note
	}
	return g
}

// playerSearch is the window's state: the filters (S2 defaults: any race,
// any class, levels 1-60), the selected row and the column sort.
type playerSearch struct {
	ready            bool
	race, class      int // -1 = Any
	minLevel, maxLev int
	selected         int // index into the sorted rows; -1 none
	sortCol          int // -1 server order, 0 name, 1 race, 2 class, 3 level
	sortDesc         bool
	scroll           int
}

func newPlayerSearch() playerSearch {
	return playerSearch{ready: true, race: -1, class: -1, minLevel: 1, maxLev: 60, selected: -1, sortCol: -1}
}

func (c *Client) drawMenuPlayerSearch(startY, contentH int32) {
	gs := c.engine.State
	if gs == nil {
		return
	}
	ps := &c.playerSearch
	if !ps.ready {
		*ps = newPlayerSearch()
	}
	px := menuXPos() + menuPad
	w := menuW - menuPad*2
	lineH := int32(18)
	y := startY

	// Title, and the Group menu's Register / Unregister (0x1503 / 0x1504).
	c.uiLabel(px, y+2, "Search for Player", 14, colorGold)
	if c.uiButtonStyled(px+w-200, y, 96, 18, "Register", 12) {
		c.engine.SendPlayerSearchRegister(true)
	}
	if c.uiButtonStyled(px+w-98, y, 96, 18, "Unregister", 12) {
		c.engine.SendPlayerSearchRegister(false)
	}
	y += lineH + 8

	// The filter row: Race, Class, Min.Lv, Max.Lv, Search. A click on a
	// name steps it on, wrapping through Any.
	raceLabel, classLabel := "Any", "Any"
	if ps.race >= 0 {
		raceLabel = raceName(ps.race)
	}
	if ps.class >= 0 {
		classLabel = className(ps.class)
	}
	c.uiLabel(px, y+3, "Race", 11, colorDim)
	if c.uiButtonStyled(px+36, y, 96, 18, raceLabel, 12) {
		if ps.race++; ps.race >= len(searchRaceShort) {
			ps.race = -1
		}
	}
	c.uiLabel(px+142, y+3, "Class", 11, colorDim)
	if c.uiButtonStyled(px+178, y, 116, 18, classLabel, 12) {
		if ps.class++; ps.class >= len(searchClassShort) {
			ps.class = -1
		}
	}
	y += lineH + 6
	// The sliders print their own values.
	c.uiLabel(px, y+3, "Min.Lv", 11, colorDim)
	c.uiSliderInt(px+44, y, 84, &ps.minLevel, 1, 60)
	c.uiLabel(px+164, y+3, "Max.Lv", 11, colorDim)
	c.uiSliderInt(px+208, y, 84, &ps.maxLev, 1, 60)
	if c.uiButtonStyled(px+w-90, y-2, 88, 20, "Search", 13) {
		b := func(v int) byte {
			if v < 0 {
				return 0xFF
			}
			return byte(v)
		}
		c.engine.SendPlayerSearch(b(ps.race), b(ps.class), byte(ps.minLevel), byte(ps.maxLev))
		ps.selected = -1
		ps.scroll = 0
	}
	y += lineH + 8

	rows := make([]game.PlayerSearchEntry, len(gs.PlayerSearch))
	copy(rows, gs.PlayerSearch)
	if ps.sortCol >= 0 {
		cmp := func(a, b game.PlayerSearchEntry) int {
			switch ps.sortCol {
			case 1:
				return strings.Compare(searchShort(searchRaceShort, a.Race), searchShort(searchRaceShort, b.Race))
			case 2:
				return strings.Compare(searchShort(searchClassShort, a.Class), searchShort(searchClassShort, b.Class))
			case 3:
				return int(a.Level) - int(b.Level)
			}
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if ps.sortDesc {
				return cmp(rows[i], rows[j]) > 0
			}
			return cmp(rows[i], rows[j]) < 0
		})
	}
	if ps.selected >= len(rows) {
		ps.selected = -1
	}

	c.uiLabel(px, y, fmt.Sprintf("%d players found", len(rows)), 12, colorWhite)
	y += lineH

	// Column headers, sortable: a click sorts by the column, a second click
	// turns it round.
	cols := []struct {
		label string
		x     int32
	}{{"Name", 4}, {"Race", w * 45 / 100}, {"Class", w * 62 / 100}, {"Level", w * 82 / 100}}
	mx, my := rl.GetMouseX(), rl.GetMouseY()
	for i, col := range cols {
		label := col.label
		if ps.sortCol == i {
			label += map[bool]string{false: " ^", true: " v"}[ps.sortDesc]
		}
		c.drawText(label, px+col.x, y, 12, colorGold)
		right := px + w
		if i+1 < len(cols) {
			right = px + cols[i+1].x
		}
		if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && mx >= px+col.x && mx < right && my >= y && my < y+lineH {
			if ps.sortCol == i {
				ps.sortDesc = !ps.sortDesc
			} else {
				ps.sortCol, ps.sortDesc = i, false
			}
			ps.selected = -1
		}
	}
	y += lineH

	detailH := int32(64)
	listH := contentH - (y - startY) - detailH - 6
	if listH < lineH*3 {
		listH = lineH * 3
	}
	if clicked := c.uiScrollList(px, y, w, listH, len(rows), &ps.scroll, lineH,
		func(i int, x, ey, rw int32, hover bool) {
			e := rows[i]
			if i == ps.selected {
				rl.DrawRectangle(x, ey, rw, lineH, rl.Color{R: 50, G: 50, B: 90, A: 255})
			}
			c.drawText(e.Name, x+cols[0].x, ey+2, 13, colorPlayerName)
			c.drawText(searchShort(searchRaceShort, e.Race), x+cols[1].x, ey+2, 12, colorWhite)
			c.drawText(searchShort(searchClassShort, e.Class), x+cols[2].x, ey+2, 12, colorWhite)
			c.drawText(fmt.Sprintf("%d", e.Level), x+cols[3].x, ey+2, 12, colorWhite)
		}); clicked >= 0 {
		ps.selected = clicked
	}
	y += listH + 6

	// The selected player: Location and Group lines, Tell and Invite.
	if ps.selected < 0 {
		c.uiLabel(px, y, "Select a player.", 12, colorDim)
		return
	}
	e := rows[ps.selected]
	if e.HasLocation {
		c.uiLabel(px, y, game.LocationLine(c.words, e.World, e.Col, e.Row), 12, colorWhite)
	}
	if g := searchGroupLine(e); g != "" {
		c.uiLabel(px, y+lineH, g, 12, colorWhite)
	}
	by := y + lineH*2 + 4
	if c.uiButtonStyled(px, by, 70, 20, "Tell", 13) {
		c.menuOpen = false
		c.chatTyping = true
		c.chatBuffer = "/t " + e.Name + " "
	}
	if e.ID != 0 && c.uiButtonStyled(px+78, by, 70, 20, "Invite", 13) {
		c.engine.SendGroupInvite(e.ID, e.Name)
		c.menuOpen = false
	}
}
