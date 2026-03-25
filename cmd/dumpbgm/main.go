package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
	"github.com/eqoa/client/internal/audio"
)

func main() {
	file, err := esf.Open("/home/sdg/claude-eqoa/extracted-assets/TUNARIA.ESF")
	if err != nil {
		log.Fatal(err)
	}
	zones := audio.LoadBGMZones(file)
	byPath := map[string][]audio.BGMZone{}
	for _, z := range zones {
		byPath[z.Path] = append(byPath[z.Path], z)
	}
	for path, zs := range byPath {
		name := path
		if idx := strings.LastIndexAny(name, "\\/"); idx >= 0 {
			name = name[idx+1:]
		}
		fmt.Printf("\n%s (%d placements):\n", strings.ToUpper(name), len(zs))
		for _, z := range zs {
			fmt.Printf("  X:[%.0f, %.0f] Y:[%.0f, %.0f] Z:[%.0f, %.0f]\n",
				z.MinX, z.MaxX, z.MinY, z.MaxY, z.MinZ, z.MaxZ)
		}
	}
}
