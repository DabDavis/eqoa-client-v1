package game

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eqoa/client/internal/network"
	"github.com/eqoa/client/internal/protocol"
)

// The PS2's own captures, replayed through the whole intake: DRDP delivery
// (ProcessIncoming) and then dispatchIncoming, exactly as a live session runs
// it. Each capture must replay without a recovered panic. With
// EQOA_REPLAY_OUT=<dir> the resulting state is written out, one file per
// capture, so two builds of the intake can be diffed.
var replayCaptures = []string{
	"ps2_sandstorm.pcap",
	"ps2_sandstorm_1min10secs.pcap",
	"ps2_sandstorm_cast_spells_and_autoattack.pcap",
	"ps2_sandstorm_interact.pcap",
	"ps2_sandstorm_new_instance.pcap",
	"ps2_main_capture.pcap",
	"sandstorm-warp.pcap",
	"sandstorm-worldchange-20260807.pcap",
	"local-warp.pcap",
	"breath_capture.pcap",
	"login_position_capture.pcap",
}

func TestReplayCapturesThroughIntake(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "pcaps")
	out := os.Getenv("EQOA_REPLAY_OUT")
	for _, name := range replayCaptures {
		t.Run(name, func(t *testing.T) {
			d := replayReadUDP(t, filepath.Join(dir, name), 10070)
			if len(d) == 0 {
				t.Skip("no game-server traffic")
			}
			summary, panics := replaySessions(t, d)
			if out != "" {
				if err := os.WriteFile(filepath.Join(out, name+".txt"), []byte(summary), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if panics > 0 {
				t.Errorf("%d recovered panics:\n%s", panics, summary)
			}
		})
	}
}

func replayReadUDP(t *testing.T, path string, port uint16) (s2c [][]byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no capture: %v", err)
	}
	if len(raw) < 24 || binary.LittleEndian.Uint32(raw) != 0xa1b2c3d4 {
		t.Skipf("%s: not a little-endian pcap", path)
	}
	link := binary.LittleEndian.Uint32(raw[20:])
	for p := 24; p+16 <= len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[p+8:]))
		if p+16+n > len(raw) {
			break
		}
		frame := raw[p+16 : p+16+n]
		p += 16 + n
		var ip []byte
		switch link {
		case 1: // Ethernet
			if len(frame) < 14 || binary.BigEndian.Uint16(frame[12:]) != 0x0800 {
				continue
			}
			ip = frame[14:]
		case 113: // Linux cooked (-i any)
			if len(frame) < 16 || binary.BigEndian.Uint16(frame[14:]) != 0x0800 {
				continue
			}
			ip = frame[16:]
		case 276: // Linux cooked v2
			if len(frame) < 20 || binary.BigEndian.Uint16(frame[0:]) != 0x0800 {
				continue
			}
			ip = frame[20:]
		default:
			t.Skipf("%s: link type %d", path, link)
		}
		if len(ip) < 20 || ip[0]>>4 != 4 {
			continue
		}
		ihl := int(ip[0]&0x0F) * 4
		if ip[9] != 17 || len(ip) < ihl+8 {
			continue
		}
		udp := ip[ihl:]
		if binary.BigEndian.Uint16(udp) != port {
			continue
		}
		ulen := int(binary.BigEndian.Uint16(udp[4:]))
		if ulen > len(udp) || ulen < 8 {
			continue
		}
		s2c = append(s2c, append([]byte(nil), udp[8:ulen]...))
	}
	return s2c
}

// replaySessions replays every server endpoint that carries a session (20 or
// more packets; the lobby's few are skipped) in the order each first appears,
// each through its own Connection and GameState, and joins their summaries.
// A capture can hold several game sessions (a new instance is a new
// endpoint), so picking one by packet count is ambiguous on a tie.
func replaySessions(t *testing.T, datagrams [][]byte) (string, int) {
	t.Helper()
	var pkts []*network.ReceivedPacket
	count := map[uint16]int{}
	var order []uint16
	for _, d := range datagrams {
		pkt, err := network.ParsePacket(d, nil)
		if err != nil || pkt.Body == nil {
			continue
		}
		if count[pkt.LocalEndpoint] == 0 {
			order = append(order, pkt.LocalEndpoint)
		}
		pkts = append(pkts, pkt)
		count[pkt.LocalEndpoint]++
	}
	var b strings.Builder
	panics := 0
	for _, ep := range order {
		if count[ep] < 20 {
			continue
		}
		s, n := replayIntake(t, pkts, ep)
		fmt.Fprintf(&b, "=== endpoint %04X, %d packets\n%s", ep, count[ep], s)
		panics += n
	}
	return b.String(), panics
}

// replayIntake runs one endpoint's packets through a fresh Connection and
// GameState and summarises what they left behind.
func replayIntake(t *testing.T, pkts []*network.ReceivedPacket, ep uint16) (string, int) {
	t.Helper()
	c, err := network.NewConnection("127.0.0.1", 9, 0xFFFE)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gs := NewGameState("test", 10070, 9735, 7000, "test")
	gs.Conn = c

	var logs bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	kinds := map[string]int{}
	for _, pkt := range pkts {
		if pkt.LocalEndpoint != ep {
			continue
		}
		for _, m := range c.ProcessIncoming(pkt) {
			key := fmt.Sprintf("type %02X", byte(m.Type))
			if !m.Type.IsStateChannel() || m.Type.IsReliable() {
				if d := m.Data(); len(d) >= 2 {
					key += fmt.Sprintf(" op %04X", uint16(d[0])|uint16(d[1])<<8)
				}
			}
			kinds[key]++
			gs.dispatchIncoming(m)
		}
	}

	var b strings.Builder
	panics := strings.Count(logs.String(), "PANIC recovered")
	fmt.Fprintf(&b, "panics %d\n", panics)
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("delivered:\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s x%d\n", k, kinds[k])
	}
	fmt.Fprintf(&b, "phase %v\n", gs.Phase)
	fmt.Fprintf(&b, "player %q level %d class %d race %d pos (%.1f, %.1f, %.1f)\n",
		gs.Player.Name, gs.Player.Level, gs.Player.Class, gs.Player.Race, gs.Player.X, gs.Player.Y, gs.Player.Z)
	fmt.Fprintf(&b, "characters %d, items %d, spells %d, chat %d\n",
		len(gs.Characters), len(gs.ParsedItems), len(gs.ParsedSpells), len(gs.ChatLog))
	var ents []string
	for id, e := range gs.Entities {
		ents = append(ents, fmt.Sprintf("  %08X %q", id, e.Name))
	}
	sort.Strings(ents)
	fmt.Fprintf(&b, "entities %d:\n%s\n", len(ents), strings.Join(ents, "\n"))
	b.WriteString("chat:\n")
	for _, m := range gs.ChatLog {
		fmt.Fprintf(&b, "  %+v\n", m)
	}
	if panics > 0 {
		for _, l := range strings.Split(logs.String(), "\n") {
			if strings.Contains(l, "PANIC") {
				fmt.Fprintf(&b, "%s\n", l)
			}
		}
	}
	return b.String(), panics
}

var _ = protocol.MessageTypeReliable
