package main

import (
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// drawWrappedText draws text with word wrapping within a given width.
func (c *Client) drawWrappedText(text string, x, y, maxW, fontSize int32, color rl.Color) {
	words := strings.Fields(text)
	lineY := y
	line := ""
	for _, word := range words {
		test := line
		if test != "" {
			test += " "
		}
		test += word
		if c.measureText(test, fontSize) > maxW && line != "" {
			c.drawText(line, x, lineY, fontSize, color)
			lineY += fontSize + 2
			line = word
		} else {
			line = test
		}
	}
	if line != "" {
		c.drawText(line, x, lineY, fontSize, color)
	}
}

// Race descriptions — EQOA character creation flavor text (from Project Return Home lore).
var raceDescriptions = [10]string{
	// HUM
	"Humans are the most abundant race in Norrath. Good, neutral, or evil, " +
		"they pursue a gamut of philosophies. In the West, King Antonius Bayle II " +
		"rules Qeynos. To the East, the merchant city of Freeport chimes with " +
		"the sound of gold changing hands. Humans are an open-minded lot and " +
		"may pursue nearly any profession undeterred by racial shortcomings.",
	// ELF
	"Takish'Hiz, the ancient home of the Elves, is now a smoking ruin. " +
		"Most Elven refugees have relocated northward to the new city of " +
		"Fayspires. They appear somewhat human but have very sharp features " +
		"and pointed ears. Generally liked by most races, they will not " +
		"hesitate to join parties who lean toward good.",
	// DELF
	"The evil brethren of the woodland and high elves. They emerged from " +
		"the Underfoot and used necromancy to destroy Takish'Hiz. Their " +
		"bluish-black skin, white hair, and sharp features set them apart. " +
		"Driven by hatred and ambition, their natural magical ability makes " +
		"them feared spellcasters across all of Tunaria.",
	// GNO
	"Through exploratory mining, Gnomes found a location on the surface " +
		"to build Klick'Anon, unfortunately placing themselves in the middle " +
		"of the Elven war. They believe building contraptions ranks right up " +
		"there with saving the world. They do not believe in failures, only " +
		"temporary setbacks.",
	// DWF
	"Dwarves appear to be small, burly humans, but do not see their lack " +
		"of height as a disadvantage — it is just as satisfying to break " +
		"kneecaps as it is to break noses. They share a common belief in " +
		"good, strong ale. They remain content harvesting the fruits of the " +
		"mountains from their colony of Moradhim.",
	// TRL
	"Trolls typically stand about eight feet tall, are extremely strong, " +
		"and green of skin. They bask in the glory of killing, eating their " +
		"kills, and killing some more. With Ykesha fallen and the frogloks " +
		"retaking Guk, the city of Grobb has emerged as the most powerful " +
		"community of Trolls on Norrath.",
	// BAR
	"The frozen north has remained isolated for centuries. These proud " +
		"Northmen have united, leaving their nomadic heritage behind to " +
		"join the civilized races. A hardier variety of human, they inhabit " +
		"the cold and rugged northlands from their city of Halas. Eating, " +
		"drinking, and battle are their preferred pastimes.",
	// HLF
	"Halflings exist much as they did hundreds of years ago. Content to " +
		"live the simple lives of farmers and craftsmen, their worries " +
		"consist of nasty goblins, visiting relatives, and ruined pot " +
		"roasts. Stocky and light of foot, male halflings take great pride " +
		"in their foot hair.",
	// ERU
	"The city of the Highmen, Highbourne, has been built upon a foundation " +
		"of earth, stone, and the words of their leader, Erud. They are the " +
		"exact opposite of the barbarian Northmen, priding themselves on " +
		"intellectual abilities while largely neglecting physical disciplines. " +
		"Any obstacle can be overcome with intelligence and wisdom.",
	// OGR
	"Created by Rallos Zek during the Second Pact of the Gods, Ogres are " +
		"the largest and fiercest of all races. They embody their creator's " +
		"propensity for war. What they lack in cunning they make up for in " +
		"raw, devastating power. Their dark hearts know only the glory of " +
		"conquest and destruction.",
}

// Class descriptions — EQOA character creation flavor text.
var classDescriptions = [15]string{
	// WAR
	"Warriors are masters of martial combat, wielding heavy weapons " +
		"and wearing the strongest armor. They stand at the front line, " +
		"absorbing punishment and protecting their allies.",
	// RAN
	"Rangers are skilled woodsmen who blend combat prowess with " +
		"nature magic. They excel at ranged attacks and can track " +
		"enemies across any terrain.",
	// PAL
	"Paladins are holy warriors who combine swordsmanship with " +
		"divine magic. They can heal allies, smite undead, and " +
		"serve as stalwart defenders of the righteous.",
	// SK
	"Shadow Knights are dark warriors who wield unholy magic " +
		"alongside their blades. They drain life from enemies " +
		"and command fearsome dark powers.",
	// MNK
	"Monks are disciplined martial artists who fight with their " +
		"bare hands or simple weapons. Their speed and evasion " +
		"make them deadly opponents in close combat.",
	// BRD
	"Bards are versatile performers whose songs enhance allies " +
		"and hinder foes. They can increase movement speed, " +
		"regenerate health, and inspire courage in battle.",
	// RGE
	"Rogues are stealthy combatants who strike from the shadows. " +
		"Their high dexterity and cunning make them masters of " +
		"surprise attacks and lockpicking.",
	// DRD
	"Druids are guardians of nature who command the forces of " +
		"the wild. They heal allies, call upon elemental powers, " +
		"and can transform into animal forms.",
	// SHA
	"Shamans are spiritual leaders who commune with the spirit " +
		"world. They heal, buff allies with powerful wards, and " +
		"weaken enemies with debilitating curses.",
	// CL
	"Clerics are devoted healers and protectors blessed by the " +
		"gods. They wield the strongest healing magic and can " +
		"resurrect fallen allies in battle.",
	// MAG
	"Magicians are elemental summoners who call forth powerful " +
		"pets to fight alongside them. They command fire, earth, " +
		"water, and air to devastate their foes.",
	// NEC
	"Necromancers are dark spellcasters who command the undead. " +
		"They drain life from enemies, summon skeletal minions, " +
		"and wield powerful damage-over-time spells.",
	// ENC
	"Enchanters are masters of mind magic who mesmerize enemies " +
		"and enhance allies. Their crowd control abilities make " +
		"them invaluable in group encounters.",
	// WIZ
	"Wizards are the most powerful direct damage casters in " +
		"Norrath. They rain destruction upon enemies with " +
		"devastating elemental spells from a distance.",
	// ALC
	"Masters of forbidden chemistry, Alchemists consume dangerous " +
		"concoctions to fuel their power, hurling volatile mixtures " +
		"that devastate groups of enemies with area of effect magic. " +
		"Some choose to hone their defensive capabilities, becoming " +
		"surprisingly resilient on the front lines.",
}
