package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/eqoa/client/internal/game"
	_ "github.com/mattn/go-sqlite3"
)

type combo struct{ race, humType, class int }

var combos = []combo{
	{0,1,0},{0,1,3},{0,1,5},{0,1,6},{0,1,9},{0,1,10},{0,1,11},{0,1,12},{0,1,13},{0,1,14},
	{0,2,0},{0,2,1},{0,2,2},{0,2,4},{0,2,5},{0,2,6},{0,2,7},{0,2,9},{0,2,10},{0,2,12},{0,2,13},{0,2,14},
	{1,0,1},{1,0,2},{1,0,5},{1,0,6},{1,0,7},{1,0,9},{1,0,10},{1,0,12},{1,0,13},{1,0,14},
	{2,0,0},{2,0,3},{2,0,6},{2,0,9},{2,0,10},{2,0,11},{2,0,12},{2,0,13},{2,0,14},
	{3,0,0},{3,0,6},{3,0,9},{3,0,10},{3,0,11},{3,0,12},{3,0,13},{3,0,14},
	{4,0,0},{4,0,2},{4,0,6},{4,0,9},
	{5,0,0},{5,0,3},{5,0,8},
	{6,0,0},{6,0,6},{6,0,8},
	{7,0,0},{7,0,6},{7,0,7},{7,0,9},
	{8,0,2},{8,0,3},{8,0,9},{8,0,10},{8,0,11},{8,0,12},{8,0,13},{8,0,14},
	{9,0,0},{9,0,3},{9,0,8},{9,0,11},
}

var rn = []string{"HUM","ELF","DELF","GNO","DWF","TRL","BAR","HLF","ERU","OGR"}
var cn = []string{"WAR","RAN","PAL","SK","MNK","BRD","RGE","DRD","SHA","CL","MAG","NEC","ENC","WIZ","ALC"}

// accountPass is the game account password, from $EQOA_PASSWORD.
var accountPass string

func main() {
	if accountPass = os.Getenv("EQOA_PASSWORD"); accountPass == "" {
		log.Fatal("set EQOA_PASSWORD to the account password")
	}
	db, _ := sql.Open("sqlite3", "/tmp/default_stats.db")
	defer db.Close()
	db.Exec(`CREATE TABLE IF NOT EXISTS ds (
		race INT, ht INT, cls INT,
		str INT, sta INT, agi INT, dex INT, wis INT, intel INT, cha INT,
		bstr INT, bsta INT, bagi INT, bdex INT, bwis INT, bint INT, bcha INT,
		hp INT, pow INT, ac INT, x REAL, y REAL, z REAL, w INT, spd REAL,
		PRIMARY KEY(race,ht,cls))`)

	for i, c := range combos {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM ds WHERE race=? AND ht=? AND cls=?", c.race, c.humType, c.class).Scan(&n)
		if n > 0 {
			log.Printf("[%d/%d] %s %s ht=%d — skip", i+1, len(combos), rn[c.race], cn[c.class], c.humType)
			continue
		}
		log.Printf("[%d/%d] %s %s ht=%d", i+1, len(combos), rn[c.race], cn[c.class], c.humType)
		r := harvest(c)
		if r == nil {
			log.Printf("  FAILED")
			time.Sleep(2*time.Second)
			continue
		}
		db.Exec(`INSERT OR REPLACE INTO ds VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.race, c.humType, c.class,
			r.str, r.sta, r.agi, r.dex, r.wis, r.intel, r.cha,
			r.bstr, r.bsta, r.bagi, r.bdex, r.bwis, r.bint, r.bcha,
			r.hp, r.pow, r.ac, r.x, r.y, r.z, r.w, r.spd)
		log.Printf("  STR=%d(%d) STA=%d(%d) AGI=%d(%d) DEX=%d(%d) WIS=%d(%d) INT=%d(%d) CHA=%d(%d) HP=%d POW=%d",
			r.str, r.bstr, r.sta, r.bsta, r.agi, r.bagi, r.dex, r.bdex,
			r.wis, r.bwis, r.intel, r.bint, r.cha, r.bcha, r.hp, r.pow)
	}
	printSummary(db)
}

type sr struct {
	str,sta,agi,dex,wis,intel,cha int
	bstr,bsta,bagi,bdex,bwis,bint,bcha int
	hp,pow,ac int
	x,y,z float32
	w int
	spd float32
}

func harvest(c combo) *sr {
	// Phase 1: create char
	e1 := game.NewEngine("5.161.217.188", 10070, 9735, 7000, "dab", accountPass)
	e1.AutoConnect = true
	t1 := time.NewTicker(100*time.Millisecond)
	d1 := make(chan struct{})
	go func() { for { select { case <-t1.C: e1.Tick(); case <-d1: return } } }()

	if !wp(e1, game.PhaseCharSelect, 20*time.Second) {
		close(d1); t1.Stop(); return nil
	}
	nm := fmt.Sprintf("S%d%d%d", c.race, c.class, c.humType)
	e1.State.CreateCharacter(nm, c.race, c.class, 0, 0, 0, 0, 0, c.humType, [7]int{})
	time.Sleep(3*time.Second)
	e1.SendDisconnect()
	if e1.State.Conn != nil { e1.State.Conn.Close() }
	close(d1); t1.Stop()
	time.Sleep(1*time.Second)

	// Phase 2: reconnect and enter world
	e2 := game.NewEngine("5.161.217.188", 10070, 9735, 7000, "dab", accountPass)
	e2.AutoConnect = true
	t2 := time.NewTicker(100*time.Millisecond)
	d2 := make(chan struct{})
	go func() { for { select { case <-t2.C: e2.Tick(); case <-d2: return } } }()

	defer func() { e2.SendDisconnect(); if e2.State.Conn!=nil{e2.State.Conn.Close()}; close(d2); t2.Stop(); time.Sleep(1*time.Second) }()

	if !wp(e2, game.PhaseCharSelect, 20*time.Second) { return nil }
	idx := -1
	for i, ch := range e2.State.Characters { if ch.Name == nm { idx = i; break } }
	if idx < 0 { log.Printf("  '%s' not found in %d chars", nm, len(e2.State.Characters)); return nil }

	e2.State.SelectCharacter(idx)
	if !wp(e2, game.PhaseInWorld, 20*time.Second) { return nil }
	time.Sleep(5*time.Second)

	p := &e2.State.Player
	return &sr{
		str:int(p.Str), sta:int(p.Sta), agi:int(p.Agi), dex:int(p.Dex),
		wis:int(p.Wis), intel:int(p.Int), cha:int(p.Cha),
		bstr:int(p.BaseStr), bsta:int(p.BaseSta), bagi:int(p.BaseAgi), bdex:int(p.BaseDex),
		bwis:int(p.BaseWis), bint:int(p.BaseInt), bcha:int(p.BaseCha),
		hp:int(p.MaxHPInt), pow:int(p.MaxPowerInt), ac:int(p.AC),
		x:p.X, y:p.Y, z:p.Z, w:int(p.World), spd:p.Speed,
	}
}

func wp(e *game.Engine, ph game.Phase, to time.Duration) bool {
	dl := time.Now().Add(to)
	for time.Now().Before(dl) { if e.State.Phase == ph { return true }; time.Sleep(200*time.Millisecond) }
	return false
}

func printSummary(db *sql.DB) {
	rows, _ := db.Query("SELECT race,ht,cls,str,sta,agi,dex,wis,intel,cha,bstr,bsta,bagi,bdex,bwis,bint,bcha,hp,pow FROM ds ORDER BY race,ht,cls")
	defer rows.Close()
	fmt.Printf("\n%-4s %s %-3s  STR(b)  STA(b)  AGI(b)  DEX(b)  WIS(b)  INT(b)  CHA(b)   HP  POW\n", "Race","HT","Cls")
	for rows.Next() {
		var race,ht,cls,s,st,a,d,w,i,c,bs,bst,ba,bd,bw,bi,bc,hp,pw int
		rows.Scan(&race,&ht,&cls,&s,&st,&a,&d,&w,&i,&c,&bs,&bst,&ba,&bd,&bw,&bi,&bc,&hp,&pw)
		fmt.Printf("%-4s  %d %-3s %3d(%2d) %3d(%2d) %3d(%2d) %3d(%2d) %3d(%2d) %3d(%2d) %3d(%2d) %4d %4d\n",
			rn[race],ht,cn[cls],s,bs,st,bst,a,ba,d,bd,w,bw,i,bi,c,bc,hp,pw)
	}
}
