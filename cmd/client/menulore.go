package main

import (
	rl "github.com/gen2brain/raylib-go/raylib"
)

// Lore categories for the menu tab
var loreCategories = []string{
	"World History",
	"Races of Norrath",
	"Gods of Norrath",
}

var loreWorldHistory = []loreEntry{
	{"The Age of Scale",
		"Veeshan, the great crystalline dragon, was the first deity to arrive on " +
			"the lifeless world of Norrath. She deemed it worthy of her brood and with a " +
			"cataclysmic swipe of her massive claws, she marked the planet and populated " +
			"it with the Dragonkind — dragons, wurms, and drakes."},
	{"The First Pact",
		"Brell Serilis arrived second and claimed the underground caves. During the " +
			"First Pact, Brell created the Dwarves, Tunare created the Elves, Prexus " +
			"created the Kedge, and Rallos Zek created the Giants."},
	{"The Second Pact",
		"Those gods excluded from the First Pact struck their own bargains. Rallos Zek " +
			"created the Ogres and Trolls. Cazic-Thule created the Lizard Men and brought " +
			"fear itself into the world. Innoruuk stole twelve Elves and twisted them with " +
			"hatred, creating the Dark Elves — the Teir'Dal."},
	{"The Rise of Man",
		"Seeing the success of the other races, the twin gods Mithaniel and Erollisi Marr " +
			"created the Barbarians in the frigid north. Humans emerged as the most adaptable " +
			"of all races, spreading across Tunaria and founding great cities."},
	{"The Fall of Takish'Hiz",
		"The Dark Elves used necromancy to destroy Takish'Hiz, the ancient Elven capital. " +
			"The surviving Elves fled northward and established the city of Fayspires. The war " +
			"between light and dark Elves continues to rage across eastern Tunaria."},
}

var loreRaces = []loreEntry{
	{"Humans",
		"The most abundant race in Norrath. Good, neutral, or evil, they pursue a gamut " +
			"of philosophies. In the West, King Antonius Bayle II rules Qeynos. To the East, " +
			"the merchant city of Freeport chimes with the sound of gold changing hands."},
	{"Elves",
		"Takish'Hiz, their ancient home, is now a smoking ruin. Most Elven refugees have " +
			"relocated to Fayspires. They appear somewhat human but have very sharp features " +
			"and pointed ears. Generally liked by most races."},
	{"Dark Elves",
		"The evil brethren of the woodland and high elves. They emerged from the Underfoot " +
			"and used necromancy to destroy Takish'Hiz. Their bluish-black skin, white hair, " +
			"and sharp features set them apart. Driven by hatred and ambition."},
	{"Gnomes",
		"Through exploratory mining, Gnomes found a location to build Klick'Anon, " +
			"unfortunately placing themselves in the middle of the Elven war. They believe " +
			"building contraptions ranks right up there with saving the world."},
	{"Dwarves",
		"Small, burly folk who do not see their lack of height as a disadvantage — it is " +
			"just as satisfying to break kneecaps as it is to break noses. They share a " +
			"common belief in good, strong ale from their colony of Moradhim."},
	{"Trolls",
		"Standing about eight feet tall, extremely strong, and green of skin. They bask " +
			"in the glory of killing, eating their kills, and killing some more. The city " +
			"of Grobb has emerged as their most powerful community."},
	{"Barbarians",
		"These proud Northmen have united, leaving their nomadic heritage behind. A hardier " +
			"variety of human, they inhabit the cold northlands from Halas. Eating, drinking, " +
			"and battle are their preferred pastimes."},
	{"Halflings",
		"Content to live the simple lives of farmers and craftsmen. Their worries consist of " +
			"nasty goblins, visiting relatives, and ruined pot roasts. Stocky and light of " +
			"foot, male halflings take great pride in their foot hair."},
	{"Erudites",
		"The Highmen of Highbourne, built upon the words of their leader Erud. They are " +
			"the exact opposite of the barbarian Northmen, priding themselves on intellectual " +
			"abilities while largely neglecting physical disciplines."},
	{"Ogres",
		"Created by Rallos Zek during the Second Pact, Ogres are the largest and fiercest " +
			"of all races. They embody their creator's propensity for war. What they lack " +
			"in cunning they make up for in raw, devastating power."},
}

var loreGods = []loreEntry{
	{"Veeshan — The Wurm Queen",
		"First to arrive on Norrath. She marked the planet with her claws and created " +
			"the Dragonkind. She remains aloof from mortal affairs, dwelling in the " +
			"Plane of Sky."},
	{"Tunare — Mother of All",
		"Goddess of nature and growth. She created the Elves to protect the surface " +
			"world and remains their patron deity. Her followers revere the natural world."},
	{"Brell Serilis — The Gnome King",
		"God of the underfoot. He created the Dwarves and Gnomes, claiming the " +
			"underground realms as his domain. Master of earth and stone."},
	{"Rallos Zek — The Warlord",
		"God of war. He created the Giants, Ogres, and Trolls — races that embody " +
			"his love of conflict and destruction. His domain is the Plane of War."},
	{"Innoruuk — The Prince of Hate",
		"God of hatred. He stole twelve Elves and corrupted them into the Dark Elves, " +
			"the Teir'Dal. His very essence is malice and suffering."},
	{"Cazic-Thule — The Faceless",
		"God of fear. He created the Lizard Men and rules the Plane of Fear. His " +
			"followers spread terror across Norrath. The Isle of Dread is one of his " +
			"manifestations."},
	{"Mithaniel Marr — The Truthbringer",
		"God of valor and truth. Together with his twin sister Erollisi, he created " +
			"the Barbarians in the frigid north. Champion of honor and justice."},
	{"Erollisi Marr — The Queen of Love",
		"Goddess of love and compassion. Twin sister of Mithaniel Marr. She helped " +
			"create the Barbarians and inspires acts of kindness across Norrath."},
	{"Prexus — The Ocean Lord",
		"God of the seas. He claimed Norrath's oceans during the First Pact and " +
			"created the Kedge, an aquatic race. Sailors pray to him for safe passage."},
	{"Bristlebane — The King of Thieves",
		"God of mischief and trickery. Patron of rogues and pranksters. He created " +
			"the Halflings, imbuing them with his love of good humor and adventure."},
}

type loreEntry struct {
	title string
	text  string
}

// drawMenuLore renders the Lore tab content.
func (c *Client) drawMenuLore(startY, contentH int32) {
	gold := colorGoldWarm
	dimGold := rl.Color{R: 140, G: 120, B: 80, A: 255}

	// If no category selected, show category list
	if c.loreCategory < 0 {
		c.drawText("Choose a topic:", menuXPos()+menuPad, startY+4, 12, dimGold)

		rowH := int32(28)
		mx := rl.GetMouseX()
		my := rl.GetMouseY()
		for i, cat := range loreCategories {
			ry := startY + 24 + int32(i)*rowH
			bx := menuXPos() + menuPad
			bw := menuW - menuPad*2

			hover := mx >= bx && mx < bx+bw && my >= ry && my < ry+rowH
			if hover {
				rl.DrawRectangle(bx, ry, bw, rowH-2, rl.Color{R: 50, G: 45, B: 70, A: 255})
			}
			if hover && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
				c.loreCategory = i
				c.menuScroll = 0
			}
			c.drawText(cat, bx+8, ry+6, 14, gold)
		}
		return
	}

	// Back button
	backX := menuXPos() + menuPad
	backW := int32(60)
	backH := int32(20)
	mx := rl.GetMouseX()
	my := rl.GetMouseY()
	backHover := mx >= backX && mx < backX+backW && my >= startY && my < startY+backH
	backColor := dimGold
	if backHover {
		backColor = gold
	}
	c.drawText("< Back", backX, startY+2, 12, backColor)
	if backHover && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		c.loreCategory = -1
		c.menuScroll = 0
		return
	}

	// Category title
	catTitle := loreCategories[c.loreCategory]
	titleW := c.measureText(catTitle, 14)
	c.drawText(catTitle, menuXPos()+(menuW-titleW)/2, startY+2, 14, gold)

	// Get entries for selected category
	var entries []loreEntry
	switch c.loreCategory {
	case 0:
		entries = loreWorldHistory
	case 1:
		entries = loreRaces
	case 2:
		entries = loreGods
	}

	// Scrollable entry list with wrapped text
	listY := startY + 24
	_ = contentH
	entryY := listY - int32(c.menuScroll)*16

	for _, entry := range entries {
		if entryY > startY+contentH {
			break
		}
		// Title
		if entryY >= listY-16 {
			c.drawText(entry.title, menuXPos()+menuPad, entryY, 13, gold)
		}
		entryY += 18

		// Wrapped description text
		words := splitWords(entry.text)
		line := ""
		lineW := menuW - menuPad*2
		for _, word := range words {
			test := line
			if test != "" {
				test += " "
			}
			test += word
			if c.measureText(test, 11) > lineW && line != "" {
				if entryY >= listY && entryY < startY+contentH {
					c.drawText(line, menuXPos()+menuPad, entryY, 11,
						rl.Color{R: 180, G: 180, B: 195, A: 220})
				}
				entryY += 13
				line = word
			} else {
				line = test
			}
		}
		if line != "" && entryY >= listY && entryY < startY+contentH {
			c.drawText(line, menuXPos()+menuPad, entryY, 11,
				rl.Color{R: 180, G: 180, B: 195, A: 220})
		}
		entryY += 20 // gap between entries
	}

	// Scroll with mouse wheel
	wheel := rl.GetMouseWheelMove()
	if wheel != 0 {
		c.menuScroll -= int(wheel * 3)
		if c.menuScroll < 0 {
			c.menuScroll = 0
		}
	}
}

func splitWords(s string) []string {
	var words []string
	word := ""
	for _, ch := range s {
		if ch == ' ' {
			if word != "" {
				words = append(words, word)
				word = ""
			}
		} else {
			word += string(ch)
		}
	}
	if word != "" {
		words = append(words, word)
	}
	return words
}
