package game

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

// SpellFXDB holds parsed spell effects and particle definitions from SPELLFX.CSF/ESF.
type SpellFXDB struct {
	effects   map[int32]*esf.SpellEffect       // DictID → SpellEffect
	particles map[int32]*esf.ParticleDefinition // DictID → ParticleDefinition
	file      *esf.ObjFile                      // kept for texture lookups
}

// LoadSpellFX loads SPELLFX.ESF (or decompresses .CSF) from assetsDir.
func LoadSpellFX(assetsDir string) (*SpellFXDB, error) {
	// Prefer .ESF (already decompressed) over .CSF
	esfPath := filepath.Join(assetsDir, "SPELLFX.ESF")
	csfPath := filepath.Join(assetsDir, "SPELLFX.CSF")

	var file *esf.ObjFile
	var err error

	if _, statErr := os.Stat(esfPath); statErr == nil {
		file, err = esf.Open(esfPath)
		if err != nil {
			return nil, fmt.Errorf("opening SPELLFX.ESF: %w", err)
		}
	} else if _, statErr := os.Stat(csfPath); statErr == nil {
		data, decErr := esf.DecompressCSF(csfPath)
		if decErr != nil {
			return nil, fmt.Errorf("decompressing SPELLFX.CSF: %w", decErr)
		}
		file, err = esf.OpenBytes(data)
		if err != nil {
			return nil, fmt.Errorf("parsing decompressed SPELLFX: %w", err)
		}
	} else {
		return nil, fmt.Errorf("SPELLFX.ESF/CSF not found in %s", assetsDir)
	}

	return LoadSpellFXFromFile(file)
}

// LoadSpellFXFromFile loads spell effects from a pre-opened ObjFile.
func LoadSpellFXFromFile(file *esf.ObjFile) (*SpellFXDB, error) {
	db := &SpellFXDB{
		effects:   make(map[int32]*esf.SpellEffect),
		particles: make(map[int32]*esf.ParticleDefinition),
	}

	// Parse and build dictionary
	if err := file.BuildDictionary(); err != nil {
		return nil, fmt.Errorf("building SPELLFX dictionary: %w", err)
	}

	// Iterate all objects and load SpellEffects and ParticleDefinitions
	for _, info := range file.AllObjects() {
		switch info.Type {
		case esf.TypeSpellEffect:
			obj, err := file.GetObject(info)
			if err != nil {
				continue
			}
			if se, ok := obj.(*esf.SpellEffect); ok && se.DictID != 0 {
				db.effects[se.DictID] = se
			}
		case esf.TypeParticleDefinition:
			obj, err := file.GetObject(info)
			if err != nil {
				continue
			}
			if pd, ok := obj.(*esf.ParticleDefinition); ok && pd.DictID != 0 {
				db.particles[pd.DictID] = pd
			}
		}
	}

	db.file = file
	return db, nil
}

// LookupParticle returns the ParticleDefinition for the given DictID, or nil.
func (db *SpellFXDB) LookupParticle(dictID int32) *esf.ParticleDefinition {
	if db == nil {
		return nil
	}
	return db.particles[dictID]
}

// File returns the underlying ObjFile for texture loading.
func (db *SpellFXDB) File() *esf.ObjFile {
	if db == nil {
		return nil
	}
	return db.file
}

// Lookup returns the SpellEffect for the given animHash (DictID), or nil.
func (db *SpellFXDB) Lookup(animHash uint32) *esf.SpellEffect {
	if db == nil {
		return nil
	}
	return db.effects[int32(animHash)]
}

// LogEffect logs the spell effect event sequence for the given animHash.
func (db *SpellFXDB) LogEffect(animHash uint32, sourceName, targetName string) {
	if db == nil {
		return
	}
	se := db.effects[int32(animHash)]
	if se == nil {
		log.Printf("  SpellFX: animHash=0x%08X — not found in SPELLFX", animHash)
		return
	}

	total := se.TotalEvents()
	log.Printf("  SpellFX: 0x%08X — %d events [%d,%d,%d]",
		animHash, total,
		len(se.EventLists[0]), len(se.EventLists[1]), len(se.EventLists[2]))

	for li := 0; li < 3; li++ {
		events := se.EventLists[li]
		if len(events) == 0 {
			continue
		}
		listName := esf.SpellEventListName(li)
		for ei, ev := range events {
			ename := esf.SpellEventName(ev.EventType)
			detail := db.formatEventDetail(ev)
			log.Printf("    %s[%d] %s%s", listName, ei, ename, detail)
		}
	}
}

// formatEventDetail returns a formatted string with relevant event fields.
func (db *SpellFXDB) formatEventDetail(ev esf.SpellEvent) string {
	switch ev.EventType {
	case 1: // Wait
		if ev.Fields[0] != 0 {
			return fmt.Sprintf(" i0=%d", ev.Fields[0])
		}
		return ""
	case 3, 6, 8: // CreateEmitter, SetEmitterMotif, PlayTargetProjectile — field[1] = PD DictID
		ref := ev.Fields[1]
		if _, ok := db.particles[ref]; ok {
			return fmt.Sprintf(" id=%d pdRef=0x%08X", ev.Fields[0], uint32(ref))
		}
		if ref != 0 {
			return fmt.Sprintf(" id=%d ref=0x%08X", ev.Fields[0], uint32(ref))
		}
		return fmt.Sprintf(" id=%d", ev.Fields[0])
	case 4, 10, 12: // Destroy types
		return fmt.Sprintf(" id=%d", ev.Fields[0])
	case 9: // CreateSprite — field[1] = Sprite DictID
		return fmt.Sprintf(" id=%d sprRef=0x%08X", ev.Fields[0], uint32(ev.Fields[1]))
	case 11: // CreateSound — field[1] = Sound DictID
		return fmt.Sprintf(" id=%d sndRef=0x%08X", ev.Fields[0], uint32(ev.Fields[1]))
	case 2: // SetAction
		return fmt.Sprintf(" action=%d", ev.Fields[0])
	case 14: // FadeSprite
		return fmt.Sprintf(" id=%d", ev.Fields[0])
	case 15: // PlayAttack
		return ""
	default:
		return ""
	}
}

// DumpEffect prints a detailed dump of a spell effect by DictID.
func (db *SpellFXDB) DumpEffect(dictID int32) {
	if db == nil {
		fmt.Println("SpellFX not loaded.")
		return
	}
	se := db.effects[dictID]
	if se == nil {
		fmt.Printf("SpellEffect 0x%08X not found.\n", uint32(dictID))
		return
	}

	fmt.Printf("SpellEffect DictID=0x%08X (%d)  version=%d\n", uint32(se.DictID), se.DictID, se.Version)
	total := se.TotalEvents()
	fmt.Printf("  Total events: %d [%d,%d,%d]\n", total,
		len(se.EventLists[0]), len(se.EventLists[1]), len(se.EventLists[2]))

	for li := 0; li < 3; li++ {
		events := se.EventLists[li]
		listName := esf.SpellEventListName(li)
		fmt.Printf("\n  EventList %d (%s): %d events\n", li, listName, len(events))
		for ei, ev := range events {
			ename := esf.SpellEventName(ev.EventType)
			detail := db.formatEventDetail(ev)

			// Show non-zero floats
			var floatStr string
			for fi, fv := range ev.Floats {
				if fv != 0 {
					floatStr += fmt.Sprintf(" f%d=%.3f", fi, fv)
				}
			}

			fmt.Printf("    [%2d] %-24s%s%s\n", ei, ename, detail, floatStr)
		}
	}
}

// Stats returns the number of loaded effects and particle definitions.
func (db *SpellFXDB) Stats() (effects, particles int) {
	if db == nil {
		return 0, 0
	}
	return len(db.effects), len(db.particles)
}

// EffectDictIDs returns all spell effect DictIDs in sorted order for cycling.
func (db *SpellFXDB) EffectDictIDs() []int32 {
	if db == nil {
		return nil
	}
	ids := make([]int32, 0, len(db.effects))
	for id := range db.effects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
