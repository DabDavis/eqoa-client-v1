// seamtest renders terrain through the real client pipeline (multi-zone radial
// streaming, primary-then-secondary draw order, lit shader) and writes a PNG,
// so terrain seams can be reproduced and bisected without running the client.
//
// The background is magenta on purpose: a magenta pixel in the output is a HOLE
// in the terrain, which separates "geometry crack showing what's behind" from
// "texture or shading discontinuity".
//
// A built-in Hough detector scores long straight edges in each render and
// converts their spacing to world units, so a detected seam grid can be matched
// against known terrain periods (strip 15.4u, patch 125u, overlay tile 50u,
// base tile 125u, zone 2000u).
//
//	go run ./cmd/seamtest -at 17000,15000 -alt 800
//	go run ./cmd/seamtest -sweep-zones 30,50,90,100 -sweep-alts 300,800,2000 -outdir /path
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
	rl "github.com/gen2brain/raylib-go/raylib"

	"github.com/eqoa/client/internal/frustum"
	"github.com/eqoa/client/internal/terrain"
)

type params struct {
	zone      int
	at        string
	boundary  bool
	alt       float64
	fovy      float64
	yaw       float64
	hour      float64
	radius    float64
	lighting  bool
	overlays  bool
	multizone bool
	lantern   float64
	w, h      int
	out       string
}

func main() {
	iso := flag.String("iso", "/home/sdg/claude-eqoa/EverQuest - Online Adventures - Frontiers (USA).iso", "ISO path")
	world := flag.String("world", "TUNARIA.ESF", "world ESF")
	zoneIdx := flag.Int("zone", -1, "zone proxy index (-1 = pick from -at)")
	sweepZones := flag.String("sweep-zones", "", "comma zone list: render each (amortizes the ISO index parse)")
	sweepAlts := flag.String("sweep-alts", "", "comma altitude list per zone")
	sweepHours := flag.String("sweep-hours", "", "comma game-hour list per zone/altitude")
	outDir := flag.String("outdir", "/home/sdg/claude-eqoa/.seamtest-work", "output dir for sweep renders")
	at := flag.String("at", "", "world X,Z to look at (default: centre of zone)")
	boundary := flag.Bool("boundary", false, "aim at the primary zone's X-max/Z-max corner (a zone boundary)")
	alt := flag.Float64("alt", 80, "camera altitude above ground")
	fovy := flag.Float64("fovy", 45, "camera fov")
	yaw := flag.Float64("yaw", 25, "camera yaw degrees")
	hour := flag.Float64("hour", 12, "game hour 0-24 (drives sun/ambient/fog)")
	radius := flag.Float64("radius", 1200, "zone streaming radius")
	lighting := flag.Bool("lighting", true, "use lit shader")
	overlays := flag.Bool("overlays", true, "draw overlay pass")
	multizone := flag.Bool("multizone", true, "draw neighbouring zones too")
	lantern := flag.Float64("lantern", 0, "player lantern radius (0 = off); reproduces the client's radial light pool")
	w := flag.Int("w", 1093, "width")
	h := flag.Int("h", 653, "height")
	out := flag.String("out", "/home/sdg/claude-eqoa/.seamtest-work/seam.png", "output PNG (single render)")
	flag.Parse()

	rl.SetTraceLogLevel(rl.LogWarning)
	rl.InitWindow(int32(*w), int32(*h), "seamtest")
	defer rl.CloseWindow()

	f, err := esf.OpenISOFile(*iso, *world)
	if err != nil {
		log.Fatalf("open iso: %v", err)
	}
	loader, err := terrain.NewLoaderFromFile(f)
	if err != nil {
		log.Fatalf("loader: %v", err)
	}
	defer loader.Close()

	base := params{
		zone: *zoneIdx, at: *at, boundary: *boundary, alt: *alt, fovy: *fovy,
		yaw: *yaw, hour: *hour, radius: *radius, lighting: *lighting,
		overlays: *overlays, multizone: *multizone, lantern: *lantern,
		w: *w, h: *h, out: *out,
	}

	if *sweepZones == "" {
		renderOne(loader, base)
		return
	}

	fmt.Printf("%-6s %-7s %-5s %6s %8s %8s  %-26s %s\n",
		"zone", "alt", "hour", "luma", "holes%", "line", "seam spacing (units)", "file")
	for _, zs := range strings.Split(*sweepZones, ",") {
		z, err := strconv.Atoi(strings.TrimSpace(zs))
		if err != nil {
			continue
		}
		alts := []float64{base.alt}
		if *sweepAlts != "" {
			alts = nil
			for _, as := range strings.Split(*sweepAlts, ",") {
				if v, err := strconv.ParseFloat(strings.TrimSpace(as), 64); err == nil {
					alts = append(alts, v)
				}
			}
		}
		hours := []float64{base.hour}
		if *sweepHours != "" {
			hours = nil
			for _, hs := range strings.Split(*sweepHours, ",") {
				if v, err := strconv.ParseFloat(strings.TrimSpace(hs), 64); err == nil {
					hours = append(hours, v)
				}
			}
		}
		for _, a := range alts {
			for _, hr := range hours {
				p := base
				p.zone = z
				p.alt = a
				p.hour = hr
				p.out = filepath.Join(*outDir, fmt.Sprintf("z%d_a%.0f_h%.0f.png", z, a, hr))
				renderOne(loader, p)
			}
		}
	}
}

func renderOne(loader *terrain.Loader, p params) {
	proxies := loader.GetZoneProxies()

	var tx, tz float32
	primary := p.zone
	if p.at != "" {
		var x, z float64
		if _, err := fmt.Sscanf(p.at, "%f,%f", &x, &z); err != nil {
			log.Fatalf("bad -at %q", p.at)
		}
		tx, tz = float32(x), float32(z)
		if primary < 0 {
			primary = loader.FindZone(tx, tz)
		}
	} else {
		if primary < 0 {
			primary = 0
		}
		bb := proxies.Zones[primary].BBox
		tx = (bb.MinX + bb.MaxX) / 2
		tz = (bb.MinZ + bb.MaxZ) / 2
	}
	if primary < 0 {
		log.Printf("no zone contains (%.0f,%.0f)", tx, tz)
		return
	}
	if p.boundary {
		bb := proxies.Zones[primary].BBox
		tx, tz = bb.MaxX, bb.MaxZ
		if pz := loader.FindZone(tx-1, tz-1); pz >= 0 {
			primary = pz
		}
	}

	idxs := []int{primary}
	if p.multizone {
		idxs = loader.FindNearbyZones(tx, tz, float32(p.radius))
		seen := false
		for _, i := range idxs {
			if i == primary {
				seen = true
			}
		}
		if !seen {
			idxs = append(idxs, primary)
		}
	}
	for _, i := range idxs {
		loader.EnsureZoneLoaded(i)
	}
	deadline := time.Now().Add(120 * time.Second)
	loaded := map[int]*terrain.ZoneTerrain{}
	for time.Now().Before(deadline) && len(loaded) < len(idxs) {
		for _, i := range idxs {
			if _, ok := loaded[i]; ok {
				continue
			}
			if zt := loader.GetZoneTerrain(i); zt != nil {
				loaded[i] = zt
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	zt := loaded[primary]
	if zt == nil {
		log.Printf("primary zone %d never loaded", primary)
		return
	}

	groundY, sampled := zt.SampleHeight(tx, tz, 0)
	if !sampled {
		// SampleHeight needs collision geometry; fall back to the mean Y of the
		// base-layer render triangles.
		var sum float64
		var n int
		for _, tile := range zt.Tiles {
			for _, t := range tile.Triangles {
				if t.Layer != 0 {
					continue
				}
				sum += float64(t.Y0 + t.Y1 + t.Y2)
				n += 3
			}
		}
		if n > 0 {
			groundY = float32(sum / float64(n))
		}
	}

	rend := terrain.NewRenderer()
	rend.LightingOn = p.lighting
	rend.OverlaysOff = !p.overlays
	for i, z := range loaded {
		for k := 0; k < 20000 && !rend.IsZoneBuilt(i); k++ {
			rend.UploadZoneBatch(i, z, 200)
		}
	}
	defer rend.Cleanup()

	yawRad := p.yaw * math.Pi / 180
	eye := rl.Vector3{
		X: tx + float32(p.alt*0.35*math.Cos(yawRad)),
		Y: groundY + float32(p.alt),
		Z: tz + float32(p.alt*0.35*math.Sin(yawRad)),
	}
	camera := rl.Camera3D{
		Position:   eye,
		Target:     rl.Vector3{X: tx, Y: groundY, Z: tz},
		Up:         rl.Vector3{X: 0, Y: 1, Z: 0},
		Fovy:       float32(p.fovy),
		Projection: rl.CameraPerspective,
	}

	for i := 0; i < 3; i++ {
		rl.BeginDrawing()
		rl.ClearBackground(rl.Color{R: 255, G: 0, B: 255, A: 255}) // holes show as magenta
		rl.BeginMode3D(camera)
		if p.lantern > 0 {
			// Same call the client makes for the local player's lantern.
			rend.SetPlayerLight(true, tx, groundY+2.5, tz, float32(p.lantern),
				1.0, 0.69, 0.36)
		}
		rend.SetLightingUniforms(eye.X, eye.Y, eye.Z, float32(p.hour))
		fr := frustum.FromCamera(camera, float32(p.w)/float32(p.h))
		rend.DrawZone(primary, &fr, true, nil) // primary first, as render.go does
		for i := range loaded {
			if i != primary {
				rend.DrawZone(i, &fr, false, nil)
			}
		}
		rl.EndMode3D()
		rl.EndDrawing()
	}

	img := rl.LoadImageFromScreen()
	rl.ExportImage(*img, p.out)
	cols := rl.LoadImageColors(img)

	holePct, lineScore, spacing, meanLuma := analyze(cols, p.w, p.h)
	unitsPerPx := 2 * p.alt * math.Tan(p.fovy*math.Pi/360) / float64(p.h)

	var sp []string
	for _, s := range spacing {
		sp = append(sp, fmt.Sprintf("%.1f", s*unitsPerPx))
	}
	spTxt := "-"
	if len(sp) > 0 {
		spTxt = strings.Join(sp, ", ")
	}
	fmt.Printf("%-6d %-7.0f %-5.0f %6.0f %7.3f%% %8d  %-26s %s\n",
		primary, p.alt, p.hour, meanLuma, holePct, lineScore, spTxt, filepath.Base(p.out))
	rl.UnloadImageColors(cols)
	rl.UnloadImage(img)
}

// analyze returns the percentage of background (hole) pixels, the strongest
// straight-edge score from a Hough vote over gradient pixels, and the pixel
// spacings between the strongest parallel lines at that angle.
func analyze(cols []rl.Color, w, h int) (holePct float64, bestCount int, spacing []float64, meanLuma float64) {
	luma := make([]float64, w*h)
	holes := 0
	for i := 0; i < w*h && i < len(cols); i++ {
		c := cols[i]
		if c.R > 200 && c.G < 60 && c.B > 200 {
			holes++
		}
		luma[i] = 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
	}
	holePct = 100 * float64(holes) / float64(w*h)
	for _, l := range luma {
		meanLuma += l
	}
	if len(luma) > 0 {
		meanLuma /= float64(len(luma))
	}

	// Gradient pixels: local maxima of |d/dx| + |d/dy| above a threshold.
	type pt struct{ x, y float64 }
	var edges []pt
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx := luma[y*w+x+1] - luma[y*w+x-1]
			gy := luma[(y+1)*w+x] - luma[(y-1)*w+x]
			if math.Abs(gx)+math.Abs(gy) > 18 {
				edges = append(edges, pt{float64(x), float64(y)})
			}
		}
	}
	if len(edges) < 50 {
		return holePct, 0, nil, meanLuma
	}

	// Hough: for each angle, bin edge pixels by perpendicular distance.
	bestTheta, bestBins := 0.0, map[int]int(nil)
	for deg := 0; deg < 180; deg++ {
		th := float64(deg) * math.Pi / 180
		ct, st := math.Cos(th), math.Sin(th)
		bins := map[int]int{}
		for _, e := range edges {
			d := int(math.Round(e.x*ct + e.y*st))
			bins[d]++
		}
		mx := 0
		for _, c := range bins {
			if c > mx {
				mx = c
			}
		}
		if mx > bestCount {
			bestCount, bestTheta, bestBins = mx, th, bins
		}
	}
	_ = bestTheta

	// Distances of strong parallel lines -> spacings between consecutive ones.
	if bestBins != nil && bestCount > 0 {
		var strong []int
		for d, c := range bestBins {
			if float64(c) > 0.5*float64(bestCount) {
				strong = append(strong, d)
			}
		}
		sort.Ints(strong)
		// collapse runs of adjacent bins into single lines
		var lines []float64
		for i := 0; i < len(strong); i++ {
			j := i
			for j+1 < len(strong) && strong[j+1]-strong[j] <= 3 {
				j++
			}
			lines = append(lines, float64(strong[i]+strong[j])/2)
			i = j
		}
		for i := 1; i < len(lines) && i <= 4; i++ {
			spacing = append(spacing, lines[i]-lines[i-1])
		}
	}
	return holePct, bestCount, spacing, meanLuma
}
