package game

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eqoa/client/internal/network"
)

// What a real PS2 SENT, read back out of the captures: the oracle for every
// builder in the egress pass. The client's datagrams go to the game server's
// port 10070; each client (one UDP source port) is run through its own
// Connection, which decodes a client datagram exactly as it decodes a server's
// (the DRDP header is symmetric), XOR delta and RLE included. A capture that
// begins after its session did cannot decode the 0x40 deltas (their reference
// base was never seen); those are reported as dropped, not guessed at.

// egressCaptures are the captures holding PS2 client traffic.
var egressCaptures = []string{
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

// egressMsg is one message a PS2 sent.
type egressMsg struct {
	Capture  string
	Client   uint16 // the client's UDP source port
	Datagram int    // index of the datagram within that client's stream
	Time     float64
	Type     byte   // DRDP message type: < 0xF8 a state channel, 0xFB reliable
	Opcode   uint16 // reliable/unreliable messages only
	Data     []byte // the whole message; for an opcode message, from the opcode on
}

// IsState reports whether m is a state-channel update (0x40 is the player's own).
func (m egressMsg) IsState() bool { return m.Type < 0xF8 }

// Payload is the message body after the 2-byte opcode.
func (m egressMsg) Payload() []byte {
	if m.IsState() || len(m.Data) < 2 {
		return m.Data
	}
	return m.Data[2:]
}

type c2sDatagram struct {
	port uint16
	t    float64
	data []byte
}

// readC2S returns every UDP datagram sent TO port, with its source port and time.
func readC2S(t *testing.T, path string, port uint16) []c2sDatagram {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no capture: %v", err)
	}
	if len(raw) < 24 || binary.LittleEndian.Uint32(raw) != 0xa1b2c3d4 {
		t.Skipf("%s: not a little-endian pcap", path)
	}
	link := binary.LittleEndian.Uint32(raw[20:])
	var out []c2sDatagram
	for p := 24; p+16 <= len(raw); {
		ts := float64(binary.LittleEndian.Uint32(raw[p:])) + float64(binary.LittleEndian.Uint32(raw[p+4:]))/1e6
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
		if len(ip) < 20 || ip[0]>>4 != 4 || ip[9] != 17 {
			continue
		}
		ihl := int(ip[0]&0x0F) * 4
		if len(ip) < ihl+8 {
			continue
		}
		udp := ip[ihl:]
		if binary.BigEndian.Uint16(udp[2:]) != port {
			continue
		}
		ulen := int(binary.BigEndian.Uint16(udp[4:]))
		if ulen > len(udp) || ulen < 8 {
			continue
		}
		out = append(out, c2sDatagram{binary.BigEndian.Uint16(udp), ts, append([]byte(nil), udp[8:ulen]...)})
	}
	return out
}

// egressFromCapture decodes every message the PS2 client(s) in a capture sent
// to the game server, in order, with the number of 0x40 deltas it could not
// decode (a missing reference base).
func egressFromCapture(t *testing.T, name string) (msgs []egressMsg, undecoded int) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "pcaps", name)
	dgs := readC2S(t, path, 10070)
	byPort := map[uint16][]c2sDatagram{}
	var order []uint16
	for _, d := range dgs {
		if _, ok := byPort[d.port]; !ok {
			order = append(order, d.port)
		}
		byPort[d.port] = append(byPort[d.port], d)
	}

	var logs strings.Builder
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	for _, port := range order {
		c, err := network.NewConnection("127.0.0.1", 9, 0xFFFE)
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range byPort[port] {
			pkt, err := network.ParsePacket(d.data, nil)
			if err != nil || pkt.Body == nil {
				continue
			}
			for _, m := range c.ProcessIncoming(pkt) {
				data := append([]byte(nil), m.Data()...)
				em := egressMsg{Capture: name, Client: port, Datagram: i, Time: d.t, Type: byte(m.Type), Data: data}
				if !em.IsState() && len(data) >= 2 {
					em.Opcode = binary.LittleEndian.Uint16(data)
				}
				msgs = append(msgs, em)
			}
		}
		c.Close()
	}
	undecoded = strings.Count(logs.String(), "does not exist, segment dropped")
	return msgs, undecoded
}

// egressAll is every decoded PS2 message across egressCaptures.
func egressAll(t *testing.T) []egressMsg {
	t.Helper()
	var all []egressMsg
	for _, name := range egressCaptures {
		path := filepath.Join("..", "..", "..", "pcaps", name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		m, _ := egressFromCapture(t, name)
		all = append(all, m...)
	}
	return all
}

// TestEgressCaptureCensus prints what the PS2 sent, per capture (go test -v),
// and pins that the three whole-session captures decode.
func TestEgressCaptureCensus(t *testing.T) {
	for _, name := range egressCaptures {
		t.Run(name, func(t *testing.T) {
			msgs, undecoded := egressFromCapture(t, name)
			kinds := map[string]int{}
			for _, m := range msgs {
				k := fmt.Sprintf("state %02X len %d", m.Type, len(m.Data))
				if !m.IsState() {
					k = fmt.Sprintf("type %02X op %04X", m.Type, m.Opcode)
				}
				kinds[k]++
			}
			keys := make([]string, 0, len(kinds))
			for k := range kinds {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			for _, k := range keys {
				fmt.Fprintf(&b, "  %s x%d\n", k, kinds[k])
			}
			t.Logf("%d messages, %d undecoded 0x40 deltas\n%s", len(msgs), undecoded, b.String())
			switch name {
			case "ps2_sandstorm.pcap", "ps2_main_capture.pcap", "ps2_sandstorm_1min10secs.pcap":
				if kinds["state 40 len 41"] < 300 {
					t.Errorf("%s: %d decoded 0x40 updates, want the whole session", name, kinds["state 40 len 41"])
				}
			}
		})
	}
	_ = io.Discard
}
