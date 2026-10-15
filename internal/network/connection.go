package network

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// Connection represents a DRDP UDP connection to the game server.
type Connection struct {
	conn *net.UDPConn
	addr *net.UDPAddr

	mu sync.Mutex

	// Endpoint IDs
	localEndpoint  uint16 // Our endpoint (client-assigned)
	remoteEndpoint uint16 // Server endpoint we're connecting to

	// Instance tracking — PS2 stores instance_local and instance_remote separately.
	// instance_local (our instance) goes in segment header HasInstance field.
	// instance_remote (server's instance) goes in body SessionAck field.
	instanceID      uint32 // Our instance (instance_local) — segment header
	instanceRemote  uint32 // Server's instance (instance_remote) — body SessionAck
	instanceCounter uint32 // Counter for generating sequential instance IDs
	sessionID       uint32 // From server after promotion (= player ObjectID)
	hasInstance      bool
	needsNewInst    bool
	didServerInit   bool

	// Server's actual endpoint (learned from server responses)
	serverEndpoint uint16

	// Server's packed endpoint value (from DidServerInitiate local_endpoint varint).
	// PS2 sends this back as remote_endpoint with REMOTE_EP_INLINE (0x0800) flag.
	// The Java server uses it for ConnLookup routing — without it, post-promotion
	// packets are silently dropped because the routing key doesn't match.
	serverEndpointPacked uint64

	// Sequence tracking (outgoing)
	sendArrival  uint16 // Outgoing packet sequence
	sendReliable uint16 // Outgoing reliable message sequence

	// Sequence tracking (incoming)
	recvArrival  uint16 // Last received server packet arrival number
	recvReliable uint16 // Highest contiguous reliable seq (for guaranteed_ack)

	// Out-of-order reliable message tracking: bitmap of received sequences
	// above recvReliable. When seq arrives out of order (gap), it's marked
	// here. When the gap fills, recvReliable advances past the contiguous run.
	reliableOOO map[uint16]bool

	// Server's ack of our reliable messages
	serverAckedReliable uint16

	// Last guaranteed ack we sent (to avoid resending stale values)
	lastSentReliable uint16

	// PS2 arrival history bitmap: 64-bit sliding window tracking which of the
	// last 64 arrivals were received. Used for FlushAck mask (body flag 0x04).
	// PS2 connection_record_segment: on new arrival: arrivalHistory = (arrivalHistory << 1) | 1
	// On gap: arrivalHistory <<= gapSize. On old arrival: set bit at position.
	arrivalHistory uint64

	// Persistent state channel ACKs: channel → highest acked seq.
	// PS2 uses per-channel dirty counter (decremented per flush). We track
	// the same with stateAckDirtyCount: set to 1 on receive, decremented on send.
	stateAckMap        map[byte]uint16
	stateAckDirtyCount map[byte]byte // Per-channel dirty counter (PS2: state_channel[ch].ack_dirty)

	// Outgoing message queue
	outgoing []*protocol.Message

	// Unacked reliable messages (for retransmit)
	unacked []*protocol.Message

	// Per-message send timestamps for PS2-accurate retransmit (#26).
	// PS2 tracks per-message timers; retransmits ALL expired messages per flush.
	unackedSendTime map[uint16]time.Time

	// Fragment reassembly: buffer fragments by sequence number, then
	// assemble in seq order when final 0xFB arrives. This handles UDP
	// out-of-order delivery where fragment chunks arrive in wrong order.
	fragmentParts     map[uint16][]byte // seq → chunk data
	fragmentFinalSeq  uint16           // seq of 0xFB final fragment (0 = none yet)
	fragmentStartTime time.Time        // when first fragment was buffered (for 30s cleanup)

	// Incoming parsed packets channel
	Incoming chan *ReceivedPacket

	// XOR delta bases for state channels (channel -> seq -> data)
	stateBases map[byte]map[uint16][]byte
	xorBuf     [512]byte // reusable XOR decode buffer (entity=162, stats=268+)

	// Outgoing state channel sequence counters (per channel)
	sendStateSeq map[byte]uint16

	// (prevClientUpdate/prevClientUpdateSeq removed — always send keyframes now)

	// Session ack tracking: set when server acks our instance (body flag 0x40),
	// tells us to echo our instanceID in body SessionAck going forward
	gotSessionAck bool

	// PS2 connection_record_segment: flags |= 0x09 (bits 0+3) when NewInstance received.
	// Bit 3 → body flag 0x40 (SessionAck) in connection_flush. Cleared after flush.
	// Unlike gotSessionAck (persistent), this is set per-arrival and cleared per-flush (#27).
	sessionAckPending bool

	// Handshake complete: set after we've sent enough HasInstance+SessionAck packets
	// for the server to set connection.flags & 0x40 (required for ACK processing).
	// The server needs AT LEAST 2 packets with HasInstance+SessionAck:
	//   Packet 1: server sets instance_remote from header HasInstance,
	//             but body.instance_local_ok=false (instance_remote was 0 before recordSegment)
	//   Packet 2: instance_remote now matches our SessionAck instance → instance_local_ok=true
	//             → recordAcks called → connection.flags |= 0x40
	// After flags & 0x40 is set, the server no longer requires SessionAck.
	handshakeComplete bool

	// Count of packets sent with HasInstance+SessionAck during handshake.
	// Need at least 2 for the server to bootstrap flags & 0x40; we send 5 for safety.
	handshakeSentCount int

	// PS2 conn.flags bits 0-3: set when new arrivals processed, cleared after flush.
	// Gates FlushAck/GuaranteedAck: PS2 only sends these when arrivals received since last flush.
	arrivalDirty bool

	// Connection health: track when we last received a server packet
	lastRecvTime time.Time

	// PS2 connection_flush: offset 0xa4 = time of last successful flush.
	// Updated at the END of every flush (line 1296). Used for 240s timeout
	// check (line 1280): if (time_last_flush + 240000 < current_time) → assert.
	lastFlushTime time.Time

	// PS2 keepalive: 30 seconds since last guaranteed (reliable) message send.
	// PS2 conn+0xB0 is set in connection_send_guaranteed, checked in connection_flush.
	lastGuaranteedSendTime time.Time

	// PS2 RTT-adaptive retransmit: per-seq send timestamps + min-RTT from 16-sample buffer.
	// PS2 stores timestamps at conn+0x120..0x520 (1024 entries, indexed by flush_seq & 0x3FF).
	// connection_record_acks reads these to compute RTT when FlushAck arrives.
	// PS2 RTT: 16-sample circular buffer (uint16_t[16] at offset 0x1128), min at 0x1124.
	flushSendTimes [1024]time.Time
	rttSamples     [16]time.Duration // PS2: 16-sample circular buffer (#25)
	rttSampleIdx   byte              // PS2: index at offset 0x1122, wraps with & 0x0F
	rto            time.Duration     // Retransmit timeout (min 200ms, max 10s, initial 2s)

	// PS2 body flag 0x20 ("short" flag): tracks consecutive good arrivals.
	// PS2 sets 0x20 when this counter < 17, clears it after 17+ good arrivals.
	// This affects server-side flow control (connection.flags bit 4).
	goodArrivalCount int

	// PS2 connection_record_segment line 1052-1057: when remote body flag 0x20 (short) IS set,
	// clear conn.flags bit 4 (0x10). When NOT set, set bit 4.
	// Note: bit 4 does NOT gate state ACKs (that's bit 2). Bit 4's purpose is tracked
	// here for PS2 accuracy but its consumer is in un-decompiled code.
	serverShort bool

	// Debug: packet counter for logging first N packets after switch
	pktCount int

	// Debug: message type counts for diagnostics
	msgTypeCounts map[byte]int
	totalPktsIn   int
	totalMsgsIn   int
	lastSummary   int // arrival at last summary

	// PS2 desired_flush_time (#26): Set to now + 100ms when conn.flags bits 0-3
	// transition from all-zero (first new arrival after flush). ACK-only packets
	// are delayed until this timer fires, batching multiple arrivals together.
	ackFlushDeadline time.Time

	// PS2 240-second connection timeout (#27): connection_flush checks
	// (time_last_flush + 240000) - current_time < 1 (offset 0xa4). Set when exceeded.
	timedOut bool

	// Lobby session: pre-promotion game server connection that Sandstorm keeps
	// alive alongside the promoted session. Without periodic ACKs, Sandstorm
	// times out the lobby session after ~60 seconds and tears down the connection.
	lobby *LobbySession

	// Protocol health metrics
	metrics ConnectionMetrics

	// Connection state
	connected bool
	closed    bool
}

// ConnectionMetrics tracks protocol health counters for diagnostics.
type ConnectionMetrics struct {
	PacketsRecv      uint32 // Total accepted packets
	PacketsDropped   uint32 // Incoming channel overflow drops
	ArrivalGaps      uint32 // Forward arrivals with gap > 1
	Retransmits      uint32 // Reliable messages retransmitted
	FragmentsExpired uint32 // Orphaned fragment buffers cleaned up
}

// LobbySession tracks the pre-promotion game server session for ACK maintenance.
// After session promotion (DidServerInitiate), Sandstorm keeps the original game
// server endpoint alive for keepalive/ping. The PS2 DRDP layer manages multiple
// connection_t objects per endpoint; this struct replicates that for the lobby.
type LobbySession struct {
	remoteEndpoint uint16 // Server's lobby endpoint (e.g., 0xB991)
	instanceID     uint32 // Our instance ID for the lobby session

	// Incoming tracking
	recvArrival      uint16          // Last received arrival from lobby
	recvReliable     uint16          // Highest contiguous reliable seq from lobby
	reliableOOO      map[uint16]bool // Out-of-order reliable sequences
	arrivalHistory   uint64          // 64-bit arrival bitmap
	arrivalDirty     bool            // Need to flush ACKs
	goodArrivalCount int             // For body flag 0x20 (short)

	// Outgoing
	sendArrival uint16 // Our arrival counter for lobby ACK packets

	// Handshake state (inherited from pre-promotion)
	handshakeComplete bool
}

// NewConnection creates a new DRDP UDP connection to the server.
func NewConnection(serverAddr string, serverPort int, remoteEndpoint uint16) (*Connection, error) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", serverAddr, serverPort))
	if err != nil {
		return nil, fmt.Errorf("resolve UDP addr: %w", err)
	}

	udpConn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("dial UDP: %w", err)
	}

	// Generate a random-ish local endpoint
	localEndpoint := uint16(time.Now().UnixNano() & 0xFFFF)
	if localEndpoint == 0 || localEndpoint == 0xFFFE || localEndpoint == 0xFFFF {
		localEndpoint = 0x1234
	}

	// Generate instance ID: PS2 uses (counter << 16) | localEndpoint
	instanceID := uint32(0x00020000) | uint32(localEndpoint)

	c := &Connection{
		conn:               udpConn,
		addr:               addr,
		localEndpoint:      localEndpoint,
		remoteEndpoint:     remoteEndpoint,
		instanceID:         instanceID,
		instanceCounter:    2, // PS2 starts at 2
		hasInstance:         true,
		needsNewInst:       true,
		sendArrival:        1,      // PS2/Java start flush_seq at 1, not 0 (arrival 0 is rejected as dup by Sandstorm)
		recvArrival:        0xFFFF, // Sentinel: no packets received yet (#22 signed compare)
		rto:                2 * time.Second, // PS2 initial RTO before first RTT sample
		Incoming:           make(chan *ReceivedPacket, 2048),
		reliableOOO:        make(map[uint16]bool),
		unackedSendTime:    make(map[uint16]time.Time),
		stateAckMap:        make(map[byte]uint16),
		stateAckDirtyCount: make(map[byte]byte),
		stateBases:         make(map[byte]map[uint16][]byte),
		sendStateSeq:       make(map[byte]uint16),
		msgTypeCounts:      make(map[byte]int),
	}

	return c, nil
}

// LocalEndpoint returns the client's local endpoint ID.
func (c *Connection) LocalEndpoint() uint16 {
	return c.localEndpoint
}

// SessionID returns the session ID (player ObjectID from server promotion).
func (c *Connection) SessionID() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// Connected returns whether the connection is established.
func (c *Connection) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// LastRecvTime returns the last time a valid packet was received from the server.
func (c *Connection) LastRecvTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRecvTime
}

// Close shuts down the connection.
func (c *Connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn != nil {
		c.conn.Close()
	}
	close(c.Incoming)
}

// QueueReliable queues a reliable message with the given opcode and data.
// Returns false if the send window is full (4096 in-flight limit).
func (c *Connection) QueueReliable(opcode protocol.GameOpcode, data []byte) bool {
	msg := &protocol.Message{}
	msg.Type = protocol.MessageTypeReliable
	msg.Opcode = opcode

	// Build message data: opcode (2 bytes) + payload
	msgData := make([]byte, 2+len(data))
	msgData[0] = byte(opcode)
	msgData[1] = byte(opcode >> 8)
	copy(msgData[2:], data)
	msg.SetData(msgData)

	c.mu.Lock()
	// PS2 connection_send_guaranteed: checks send_seq - acked_seq < 0x1000 (4096 in-flight limit).
	// If window full, PS2 returns -1 (fail). We drop the message to match.
	if c.sendReliable-c.serverAckedReliable >= 0x1000 {
		log.Printf("QueueReliable: send window full (send=%d acked=%d), dropping opcode=0x%04X (PS2 returns -1)",
			c.sendReliable, c.serverAckedReliable, uint16(opcode))
		c.mu.Unlock()
		return false
	}
	c.sendReliable++
	msg.Sequence = c.sendReliable
	c.outgoing = append(c.outgoing, msg)
	log.Printf("QueueReliable: opcode=0x%04X seq=%d dataLen=%d", uint16(opcode), msg.Sequence, len(data))
	c.mu.Unlock()
	return true
}

// QueueUnreliable queues an unreliable message.
func (c *Connection) QueueUnreliable(opcode protocol.GameOpcode, data []byte) {
	msg := &protocol.Message{}
	msg.Type = protocol.MessageTypeUnreliable
	msg.Opcode = opcode

	// Build message data: opcode (2 bytes) + payload
	msgData := make([]byte, 2+len(data))
	msgData[0] = byte(opcode)
	msgData[1] = byte(opcode >> 8)
	copy(msgData[2:], data)
	msg.SetData(msgData)

	c.mu.Lock()
	c.outgoing = append(c.outgoing, msg)
	c.mu.Unlock()
}

// QueueClientUpdate queues an outgoing state channel 0x40 message (position update).
// Always sends keyframes (refnum=0, raw data) — no XOR delta.
//
// PS2 uses XOR delta (refnum=1) for compression, but this creates a fragile chain:
// if ANY single UDP packet is lost, the server's SegmentBody.read() hits
// "refnum does not exist" → return false, rejecting the ENTIRE packet body.
// This kills ALL message processing (including FlushAck, GuaranteedAck, StateAcks)
// for up to 29 consecutive packets until the next keyframe. The server's unacked
// messages pile up for 60 seconds → Connection.java terminate(0).
//
// Keyframes avoid this entirely. The bandwidth cost is ~200 bytes/s extra (41 raw
// bytes RLE'd vs ~5 bytes for idle XOR delta, at 3.3 packets/s). Negligible.
func (c *Connection) QueueClientUpdate(data []byte) {
	msg := &protocol.Message{}
	msg.Type = protocol.MessageTypeClientUpdate // 0x40

	c.mu.Lock()
	c.sendStateSeq[0x40]++
	msg.Sequence = c.sendStateSeq[0x40]

	// Always send keyframe (raw, refnum=0). No XOR delta chain to break.
	msg.SetData(data)
	msg.XORByte = 0

	c.outgoing = append(c.outgoing, msg)
	c.mu.Unlock()
}

// RecvLoop runs in a goroutine, reading UDP packets and pushing them to the Incoming channel.
func (c *Connection) RecvLoop() {
	buf := make([]byte, 2048)
	for {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}

		c.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := c.conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			log.Printf("UDP recv error: %v", err)
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		pkt, err := ParsePacket(data, c)
		if err != nil {
			log.Printf("Parse packet error: %v", err)
			continue
		}

		// Send to channel (non-blocking)
		select {
		case c.Incoming <- pkt:
		default:
			c.mu.Lock()
			c.metrics.PacketsDropped++
			c.mu.Unlock()
			log.Printf("Warning: incoming packet channel full, dropping packet")
		}
	}
}

// ProcessIncoming handles a received packet: tracks sequences, extracts messages.
func (c *Connection) ProcessIncoming(pkt *ReceivedPacket) []*protocol.Message {
	if pkt.Body == nil {
		return nil
	}

	body := pkt.Body
	header := pkt.Header

	c.mu.Lock()

	// Debug: log first 20 packets to understand header structure
	c.pktCount++
	if c.pktCount <= 20 {
		log.Printf("ProcessIncoming[%d]: endpoints=0x%04X→0x%04X flags=0x%05X instance=0x%X bodySize=%d bodyFlags=0x%02X arrival=%d sessionID=%d msgs=%d",
			c.pktCount, pkt.LocalEndpoint, pkt.RemoteEndpoint,
			header.Flags, header.Instance, header.Size,
			body.Flags, body.ArrivalNumber, pkt.SessionID, len(body.Messages))
	}

	// Drop packets from old sessions (wrong instance ID).
	// After SwitchToGameServer, old login server retransmits still arrive with the
	// previous instance ID. Without this filter, they corrupt serverEndpoint and
	// recvArrival, causing game server packets to be dropped or mis-acknowledged.
	// Accept packets matching our instance (instance_local) OR the server's instance
	// (instance_remote), since server sends its own instance in the header.
	if c.hasInstance && header.Instance != 0 &&
		header.Instance != c.instanceID && header.Instance != c.instanceRemote {
		isSessionChange := header.Flags&protocol.SegmentFlagNewInstance != 0 ||
			header.Flags&protocol.SegmentFlagDidServerInitiate != 0
		if !isSessionChange {
			c.mu.Unlock()
			return nil
		}
	}

	// Track server's actual endpoint and update our remote endpoint to match.
	// For the initial 0xFFFE login connection, we learn the real server endpoint
	// (e.g., 0x73B0) from the first response. After SwitchToGameServer sets
	// serverEndpoint to the known game endpoint (e.g., 0xB991), we skip learning.
	if pkt.LocalEndpoint != 0 && c.serverEndpoint == 0 {
		c.serverEndpoint = pkt.LocalEndpoint
		c.remoteEndpoint = pkt.LocalEndpoint
		log.Printf("ProcessIncoming: server endpoint = 0x%04X (updated remote)", c.serverEndpoint)
	}

	// Drop packets from wrong endpoint (stale login server retransmits after switching).
	// Exception 1: session promotion packets (NewInstance/DidServerInitiate) change the
	// server's endpoint — the Java Sandstorm server creates a new endpoint for promoted
	// sessions using the client's local endpoint ID.
	// Exception 2: lobby session packets — after promotion, the original game server
	// endpoint continues sending pings/time. Route these to lobby tracking.
	if c.serverEndpoint != 0 && pkt.LocalEndpoint != c.serverEndpoint {
		// Check if this is a lobby session packet (post-promotion)
		if c.lobby != nil && pkt.LocalEndpoint == c.lobby.remoteEndpoint {
			c.processLobbyPacket(body)
			c.mu.Unlock()
			return nil
		}

		isPromotion := header.Flags&protocol.SegmentFlagNewInstance != 0 ||
			header.Flags&protocol.SegmentFlagDidServerInitiate != 0
		if isPromotion {
			// Save lobby session state BEFORE overwriting endpoints.
			// The pre-promotion game server session needs ongoing ACKs.
			if c.lobby == nil {
				c.lobby = &LobbySession{
					remoteEndpoint:    c.serverEndpoint,
					instanceID:        c.instanceID,
					recvArrival:       c.recvArrival,
					recvReliable:      c.recvReliable,
					reliableOOO:       c.reliableOOO,
					arrivalHistory:    c.arrivalHistory,
					arrivalDirty:      c.arrivalDirty,
					goodArrivalCount:  c.goodArrivalCount,
					sendArrival:       c.sendArrival,
					handshakeComplete: c.handshakeComplete,
				}
				log.Printf("Saved lobby session: endpoint=0x%04X instance=0x%X recvArrival=%d recvReliable=%d sendArrival=%d",
					c.lobby.remoteEndpoint, c.lobby.instanceID,
					c.lobby.recvArrival, c.lobby.recvReliable, c.lobby.sendArrival)
			}

			log.Printf("ProcessIncoming: server endpoint changed 0x%04X → 0x%04X (session promotion)",
				c.serverEndpoint, pkt.LocalEndpoint)
			c.serverEndpoint = pkt.LocalEndpoint
			c.remoteEndpoint = pkt.LocalEndpoint
		} else {
			c.mu.Unlock()
			return nil
		}
	}

	// Track instance from NewInstance packets (server promotions).
	// PS2 stores instance_remote (server's instance, offset 0x9C) separately from
	// instance_local (our instance, offset 0x98). Never overwrite our own instance
	// with the server's — the header HasInstance field must contain OUR instance.
	if header.Flags&protocol.SegmentFlagNewInstance != 0 && header.Instance != 0 {
		if header.Instance != c.instanceRemote {
			log.Printf("ProcessIncoming: server instance_remote = 0x%X (was 0x%X)", header.Instance, c.instanceRemote)
		}
		c.instanceRemote = header.Instance // Store server's instance for body SessionAck
		c.hasInstance = true
		// PS2 connection_record_segment: flags |= 0x09 (bits 0+3) on NewInstance (#27).
		// Bit 3 triggers body flag 0x40 (SessionAck) in the next flush.
		c.sessionAckPending = true
	}

	// Track server-initiated sessions (only log on first promotion)
	if header.Flags&protocol.SegmentFlagDidServerInitiate != 0 {
		if !c.didServerInit || c.sessionID != pkt.SessionID {
			log.Printf("ProcessIncoming: server-initiated session, sessionID=%d (ObjectID)", pkt.SessionID)
			c.fragmentParts = nil    // Clear any partial fragments from pre-session
			c.fragmentFinalSeq = 0
			c.fragmentStartTime = time.Time{}
			// Reset ALL reliable tracking for the new promoted session.
			// The server creates a new Connection with fresh counters.
			// Both incoming (recvReliable) and outgoing (sendReliable) must reset.
			log.Printf("ProcessIncoming: resetting reliable tracking (was recv=%d sent=%d sendArrival=%d)",
				c.recvReliable, c.sendReliable, c.sendArrival)
			c.recvReliable = 0
			c.reliableOOO = make(map[uint16]bool)
			c.lastSentReliable = 0
			c.goodArrivalCount = 0 // Reset for new session (PS2 resets on promotion)
			c.arrivalHistory = 0   // Reset arrival bitmap for new session
			c.handshakeComplete = false // New session needs fresh handshake
		c.handshakeSentCount = 0   // Reset handshake packet counter
			c.recvArrival = 0xFFFF // Sentinel: no packets received yet (#22)
			c.arrivalDirty = false
			c.sessionAckPending = false
			c.serverShort = false
			c.ackFlushDeadline = time.Time{}
			c.timedOut = false
			c.lastFlushTime = time.Time{} // Reset for new session (PS2 0x200 flag cleared)
			c.rttSamples = [16]time.Duration{}
			c.rttSampleIdx = 0
			c.rto = 2 * time.Second
			c.lastGuaranteedSendTime = time.Time{}
			c.flushSendTimes = [1024]time.Time{}
			c.sendReliable = 0 // Reset outgoing reliable seq for promoted session
			c.sendArrival = 1  // Reset outgoing arrival for promoted session (starts at 1, not 0)
			c.serverAckedReliable = 0
			c.unacked = nil
			c.unackedSendTime = make(map[uint16]time.Time)
			c.outgoing = nil
			// Reset state channel tracking for promoted session.
			// The server creates a new Connection with fresh state channel counters.
			// Stale ACKs from pre-promotion would reference sequences the promoted
			// connection never sent, triggering "can't ack more than we've sent".
			c.stateAckMap = make(map[byte]uint16)
			c.stateAckDirtyCount = make(map[byte]byte)
			c.sendStateSeq = make(map[byte]uint16)

			c.stateBases = make(map[byte]map[uint16][]byte)
			// Reset message type tracking for promoted session
			c.msgTypeCounts = make(map[byte]int)
			c.totalPktsIn = 0
			c.totalMsgsIn = 0
			c.lastSummary = 0

			// PS2 pcap shows: after promotion, the PS2 sends NewInstance (0x80000)
			// with the NEW (incremented) instance in the first packet, then drops
			// NewInstance in subsequent packets. The PS2 increments the COUNTER
			// (top 16 bits) for the promoted session: counter goes 2→3→4...
			// Instance format: (counter << 16) | localEndpoint
			// Example: auth=0x0001F7E2 (counter=1), game=0x0002F7E2 (counter=2)
			c.instanceCounter++
			c.instanceID = (uint32(c.instanceCounter) << 16) | uint32(c.localEndpoint)
			c.needsNewInst = true // Send NewInstance once to announce our new instance
			c.hasInstance = true
			// PS2 connection_record_segment sets flags |= 0x09 (bits 0 and 3) when
			// receiving NewInstance. Bit 3 (0x08) triggers body flag 0x40 (SessionAck)
			// in connection_flush. Java Sandstorm validates SessionAck body value
			// against connection.instance_remote (OUR instance from header HasInstance),
			// so the SessionAck must carry OUR instance (conn.instanceID).
			c.gotSessionAck = true          // Persistent: we've seen a SessionAck
			c.sessionAckPending = true      // Per-flush: cleared after sending (#27)
			log.Printf("ProcessIncoming: promotion — our new instance=0x%X, server instance_remote=0x%X",
				c.instanceID, c.instanceRemote)
		}
		c.didServerInit = true
		c.sessionID = pkt.SessionID
		// Store server's packed endpoint for REMOTE_EP_INLINE (0x0800).
		// What ParsePacket reads as "SessionID" is actually the server's local_endpoint
		// varint. PS2 echoes this back as remote_endpoint so the server can route packets.
		if c.serverEndpointPacked != pkt.SessionIDFull {
			c.serverEndpointPacked = pkt.SessionIDFull
			log.Printf("ProcessIncoming: stored serverEndpointPacked=0x%X for REMOTE_EP_INLINE", c.serverEndpointPacked)
		}
	}

	// Track arrival number with PS2-accurate signed 16-bit comparison (#22) and bitmap.
	// PS2 uses: ((pkt_arrival - arrival_number) * 0x10000) >> 0x10 (signed arithmetic)
	// to handle uint16 wrap-around at 65536.
	// PS2 connection_parse_segment_body rejects duplicates entirely (#19):
	// exact dup (diff==0), old already in bitmap, old > 64 back → skip ALL processing.
	arrivalDiff := int16(body.ArrivalNumber - c.recvArrival)
	isRejected := false
	arrivalValid := false // PS2 param3[0x104]: true only for new (forward) arrivals

	if arrivalDiff > 0 {
		// New arrival — PS2 is_ok = true, arrival_valid = true
		diff := uint16(arrivalDiff)
		// #19: PS2 rejects arrivals with increment > CONNECTION_FLUSH_WINDOW (0x1000 = 4096).
		// connection_parse_segment_body: if ((longlong)diff < 0x1001) { ... } else { log_parse_failure("increment > CONNECTION_FLUSH_WINDOW"); }
		if diff > 4096 {
			isRejected = true
		} else {
			if diff > 1 {
				c.metrics.ArrivalGaps++
			}
			if diff <= 64 {
				c.arrivalHistory = (c.arrivalHistory << uint(diff)) | (1 << uint(diff-1))
			} else {
				c.arrivalHistory = 0
			}
			c.recvArrival = body.ArrivalNumber
			// PS2 100ms ACK timer (#26): when bits 0-3 transition from all-zero,
			// set desired_flush_time = now + 100ms. Batches ACKs within the window.
			if !c.arrivalDirty && !c.sessionAckPending {
				c.ackFlushDeadline = time.Now().Add(100 * time.Millisecond)
			}
			c.arrivalDirty = true // #18: flag for FlushAck/GuaranteedAck (PS2 conn.flags bit 0)
			arrivalValid = true   // PS2 param3[0x104] = 1 — gates ACK processing
			if c.goodArrivalCount < 255 {
				c.goodArrivalCount++
			}
		}
	} else if arrivalDiff < 0 {
		// Old arrival — PS2: is_ok = false
		diff := uint16(-arrivalDiff)
		if diff <= 64 {
			bit := uint64(1) << uint(diff-1)
			if c.arrivalHistory&bit != 0 {
				// Already received (duplicate) — PS2 rejects entire packet.
				// PS2: connection_record_segment NOT called → counter NOT reset.
				isRejected = true
			} else {
				// Out-of-order: mark in bitmap.
				// PS2: connection_record_segment called, arrival_valid=0 → counter=0.
				c.arrivalHistory |= bit
				c.goodArrivalCount = 0
			}
		} else {
			// Too old (> 64 back) — PS2 rejects.
			// PS2: connection_record_segment NOT called → counter NOT reset.
			isRejected = true
		}
	} else {
		// Exact duplicate (same arrival number) — PS2 rejects entire packet.
		// PS2: connection_parse_segment_body returns -1 → connection_record_segment
		// NOT called → counter NOT reset.
		isRejected = true
	}

	// #19: PS2 rejects duplicate/old arrivals entirely — skip ACK processing and messages.
	// PS2 connection_record_segment only called for accepted segments, so time_last_arrival
	// (conn+0xa8) is NOT updated for rejected packets.
	if isRejected {
		c.mu.Unlock()
		return nil
	}

	// PS2 connection_record_segment line 1017: conn+0xa8 = current_time.
	// Only updated for segments that pass parse_segment_body (non-rejected).
	c.lastRecvTime = time.Now()
	c.metrics.PacketsRecv++

	// #24: PS2 connection_record_segment: when remote body flag 0x20 (short) IS set,
	// clear conn.flags bit 4 → suppress our state ACKs. When NOT set, set bit 4 → allow.
	c.serverShort = body.Flags&protocol.BodyFlagRdpMessage != 0

	// Process server's FlushAck (ack of our arrival packets) + RTT measurement (#21, #23)
	// #24: PS2 only processes FlushAck/GuaranteedAck for new (forward) arrivals.
	// Old-but-valid arrivals (reordered) have stale ACK values that are harmlessly
	// ignored by validation, but PS2 skips them entirely.
	if arrivalValid && body.Flags&protocol.BodyFlagFlushAck != 0 {
		// #23: Validate FlushAck doesn't exceed our send sequence
		if int16(body.FlushAck-c.sendArrival) > 0 {
			log.Printf("WARNING: server FlushAck %d > our sendArrival %d, ignoring", body.FlushAck, c.sendArrival)
		} else {
			// #21: RTT measurement from per-seq send timestamps
			sendTime := c.flushSendTimes[body.FlushAck&0x3FF]
			if !sendTime.IsZero() {
				rtt := time.Since(sendTime)
				c.updateRTT(rtt)
				c.flushSendTimes[body.FlushAck&0x3FF] = time.Time{} // Clear used timestamp
			}
		}
	}

	// Process server's guaranteed ack of our reliable messages
	// #24: PS2 only processes GuaranteedAck for new (forward) arrivals.
	if arrivalValid && body.Flags&protocol.BodyFlagGuaranteedAck != 0 {
		// #23: Validate GuaranteedAck doesn't exceed our send sequence
		if int16(body.GuaranteedAck-c.sendReliable) > 0 {
			log.Printf("WARNING: server GuaranteedAck %d > our sendReliable %d, ignoring", body.GuaranteedAck, c.sendReliable)
		} else if int16(body.GuaranteedAck-c.serverAckedReliable) > 4096 {
			// #20: PS2 rejects GuaranteedAck window > CONNECTION_FLUSH_WINDOW (0x1000 = 4096).
			// connection_record_acks: if (window > 0x1000) { log_parse_failure("too many guaranteed rx acks"); }
			log.Printf("WARNING: GuaranteedAck window %d > 4096, ignoring", int16(body.GuaranteedAck-c.serverAckedReliable))
		} else {
			c.serverAckedReliable = body.GuaranteedAck
			// Remove acked messages from unacked list + their send timestamps (#26).
			// PS2 connection_record_acks: retires contiguous ACK (GuaranteedAck seq)
			// AND individual out-of-order messages indicated by the mask bitmap.
			// Mask bit N = message at seq (GuaranteedAck - 1 - N) is acked.
			var remaining []*protocol.Message
			for _, msg := range c.unacked {
				if msg.Sequence <= body.GuaranteedAck {
					// Contiguous ACK — retired
					delete(c.unackedSendTime, msg.Sequence)
				} else if body.Flags&protocol.BodyFlagGuaranteedAckMask != 0 && body.GuaranteedAckMask != 0 {
					// Check individual mask bits for OOO ACKs
					offset := int(msg.Sequence - body.GuaranteedAck - 1)
					if offset >= 0 && offset < 64 && (body.GuaranteedAckMask>>(uint(offset)))&1 != 0 {
						// Server selectively ACK'd this OOO message via mask
						delete(c.unackedSendTime, msg.Sequence)
					} else {
						remaining = append(remaining, msg)
					}
				} else {
					remaining = append(remaining, msg)
				}
			}
			c.unacked = remaining
		}

		// Hybrid handshake completion: server's GuaranteedAck confirms ACK processing
		// is working (connection.flags & 0x40 set). This is the fast path — the 5-packet
		// count in BuildPacket is the safety fallback.
		if !c.handshakeComplete && arrivalValid {
			c.handshakeComplete = true
			log.Printf("Handshake complete: server confirmed via GuaranteedAck (sent %d packets)", c.handshakeSentCount)
		}
	}

	// Session ack - server acknowledges our instance.
	// PS2 DRDP: body flag 0x40 carries instance_local from the remote side.
	// When the server sends this, it contains the server's own instance.
	// We store it as instanceRemote for echoing back in our SessionAck.
	if body.Flags&protocol.BodyFlagSessionAck != 0 {
		// DON'T clear needsNewInst here — BuildPacket consumes it when we
		// actually send the NewInstance packet. The server's promotion packet
		// arrives with SessionAck BEFORE we've sent our HasInstance+NewInstance,
		// and clearing needsNewInst here would prevent us from ever sending it.
		if body.InstanceLocal != 0 && c.instanceRemote == 0 {
			c.instanceRemote = uint32(body.InstanceLocal)
			log.Printf("SessionAck: learned server instance_remote=0x%X from body", c.instanceRemote)
		}
		if !c.gotSessionAck {
			log.Printf("SessionAck: server confirmed our instance 0x%X, their instance=0x%X", c.instanceID, c.instanceRemote)
		}
		c.gotSessionAck = true
		// DON'T set handshakeComplete here — it's now driven by handshakeSentCount
		// in BuildPacket. The server needs multiple packets with HasInstance+SessionAck
		// before connection.flags & 0x40 is set. Setting handshakeComplete from the
		// server's first SessionAck response was the ROOT CAUSE of the 60-second
		// timeout: we stopped sending HasInstance+SessionAck before the server could
		// bootstrap its ACK processing.
	}

	// Debug: track incoming packets and messages after promotion
	if c.didServerInit {
		c.totalPktsIn++
		c.totalMsgsIn += len(body.Messages)
		for _, msg := range body.Messages {
			c.msgTypeCounts[byte(msg.Type)]++
		}

		// Log every packet's bodySize and msgs count for first 30 arrivals
		if body.ArrivalNumber <= 30 {
			log.Printf("PKT arrival=%d bodySize=%d bodyFlags=0x%02X msgs=%d flushAck=%d guaranteedAck=%d",
				body.ArrivalNumber, header.Size, body.Flags, len(body.Messages),
				body.FlushAck, body.GuaranteedAck)
		}

		// Log summary every 50 arrivals
		if int(body.ArrivalNumber)-c.lastSummary >= 50 {
			c.lastSummary = int(body.ArrivalNumber)
			log.Printf("=== MSG SUMMARY at arrival %d: totalPkts=%d totalMsgs=%d typeCounts=%v serverAckedOurs=%d retransmits=%d gaps=%d fragExpired=%d dropped=%d ===",
				body.ArrivalNumber, c.totalPktsIn, c.totalMsgsIn, c.msgTypeCounts, c.serverAckedReliable,
				c.metrics.Retransmits, c.metrics.ArrivalGaps, c.metrics.FragmentsExpired, c.metrics.PacketsDropped)
		}
	}

	c.mu.Unlock()

	// Process messages and track reliable sequences
	var messages []*protocol.Message
	for _, msg := range body.Messages {
		// Debug: log all message types for first 30 arrivals after promotion
		if c.didServerInit && body.ArrivalNumber <= 30 {
			log.Printf("MSG arrival=%d type=0x%02X seq=%d xor=%d size=%d",
				body.ArrivalNumber, byte(msg.Type), msg.Sequence, msg.XORByte, msg.Size())
		}

		// #25: PS2 skips unreliable (0xFC) messages from old-but-valid arrivals.
		// Old arrivals still deliver guaranteed and state channel messages, but
		// unreliable data is stale and would be superseded by newer data.
		if !arrivalValid && msg.Type == protocol.MessageTypeUnreliable {
			continue
		}

		// Deduplicate reliable messages: skip retransmissions we've already processed.
		// Uses a contiguous counter (recvReliable) + out-of-order bitmap (reliableOOO)
		// to correctly handle UDP packet reordering without dropping valid fragments.
		//
		// Previous bug: single high-water mark dropped valid out-of-order fragments.
		// Example: seq 24 arrives before 19-23 → recvReliable=24 → seqs 19-23 dropped
		// → MemoryDump reassembly missing 5 fragments → corrupt spell/item data.
		if msg.Type.IsReliable() && msg.Sequence > 0 {
			c.mu.Lock()
			isDupe := false
			if msg.Sequence <= c.recvReliable {
				// Already in contiguous range — definitely a duplicate
				isDupe = true
			} else if c.reliableOOO[msg.Sequence] {
				// Already received out-of-order — duplicate
				isDupe = true
			} else {
				// New message. Check if it's the next contiguous seq.
				if msg.Sequence == c.recvReliable+1 {
					// Next in sequence — advance contiguous counter
					c.recvReliable = msg.Sequence
					// Drain any buffered OOO seqs that are now contiguous
					for c.reliableOOO[c.recvReliable+1] {
						delete(c.reliableOOO, c.recvReliable+1)
						c.recvReliable++
					}
					// PS2 uses a 64-slot window for OOO tracking. Purge any
					// entries older than 64 seqs behind recvReliable — they
					// represent permanent gaps (server crash, promotion) that
					// will never fill. Without cleanup, the map grows unbounded.
					if len(c.reliableOOO) > 0 {
						for seq := range c.reliableOOO {
							if int16(c.recvReliable-seq) > 64 {
								delete(c.reliableOOO, seq)
							}
						}
					}
				} else {
					// Out-of-order — record in bitmap for dedup
					c.reliableOOO[msg.Sequence] = true
				}
			}
			c.mu.Unlock()
			if isDupe {
				continue
			}
		}

		// Handle state channel messages - track ACKs and apply XOR delta
		if msg.Type.IsStateChannel() && !msg.Type.IsReliable() {
			channel := byte(msg.Type)
			c.processStateChannel(channel, msg)
		}

		// Handle fragment reassembly with ORDERED assembly.
		// Fragments (0xFA intermediate + 0xFB final) share the reliable sequence counter
		// and each arrives in its own UDP packet. UDP packets can arrive out of order,
		// so we buffer fragments by sequence number and reassemble in seq order when
		// the final 0xFB arrives AND all intermediate seqs are present.
		//
		// Wire format: type(1) + size(varint) + seq(2) + chunk_data
		// Our parser reads first 2 bytes of chunk_data as "opcode" then restores them
		// into msg.Data(). The original opcode is in the first 2 bytes of the FIRST chunk.
		if msg.Type == protocol.MessageTypeSegmentReliable {
			// 0xFA intermediate fragment — buffer it
			c.mu.Lock()
			if c.fragmentParts == nil {
				c.fragmentParts = make(map[uint16][]byte)
			}
			if c.fragmentStartTime.IsZero() {
				c.fragmentStartTime = time.Now()
			}
			chunk := make([]byte, len(msg.Data()))
			copy(chunk, msg.Data())
			c.fragmentParts[msg.Sequence] = chunk
			// If we already have the final fragment, try to assemble
			if c.fragmentFinalSeq > 0 {
				if assembled := c.tryAssembleFragments(); assembled != nil {
					c.mu.Unlock()
					messages = append(messages, assembled)
					continue
				}
			}
			c.mu.Unlock()
			continue
		}
		if msg.Type == protocol.MessageTypeReliable {
			c.mu.Lock()
			hasPendingFragments := len(c.fragmentParts) > 0
			// Also treat this 0xFB as a potential final fragment if there are
			// unreceived reliable seqs below us (they could be 0xFA fragments
			// that haven't arrived yet due to UDP reordering).
			mightBeFragment := !hasPendingFragments && msg.Sequence > 1 &&
				c.recvReliable < msg.Sequence-1
			if hasPendingFragments || mightBeFragment {
				// This 0xFB is (or might be) the final fragment
				if c.fragmentParts == nil {
					c.fragmentParts = make(map[uint16][]byte)
				}
				if c.fragmentStartTime.IsZero() {
					c.fragmentStartTime = time.Now()
				}
				chunk := make([]byte, len(msg.Data()))
				copy(chunk, msg.Data())
				c.fragmentParts[msg.Sequence] = chunk
				c.fragmentFinalSeq = msg.Sequence
				if assembled := c.tryAssembleFragments(); assembled != nil {
					c.mu.Unlock()
					messages = append(messages, assembled)
					continue
				}
				c.mu.Unlock()
				continue
			}
			c.mu.Unlock()
		}

		messages = append(messages, msg)
	}

	return messages
}

// processStateChannel handles an incoming state channel message.
// Applies XOR delta reconstruction and updates persistent ACK map.
func (c *Connection) processStateChannel(channel byte, msg *protocol.Message) {
	decoded := msg.Data()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Log seq progression for state channels (always log to track advancement)
	if prevSeq, ok := c.stateAckMap[channel]; ok {
		if msg.Sequence != prevSeq {
			log.Printf("STATE ch=0x%02X seq %d→%d (xor=%d, %d bytes)", channel, prevSeq, msg.Sequence, msg.XORByte, len(decoded))
		}
	} else {
		log.Printf("STATE ch=0x%02X seq=%d FIRST (xor=%d, %d bytes)", channel, msg.Sequence, msg.XORByte, len(decoded))
	}

	// Initialize base map for this channel if needed
	if c.stateBases[channel] == nil {
		c.stateBases[channel] = make(map[uint16][]byte)
	}

	// XOR reconstruction
	if msg.XORByte != 0 {
		baseSeq := msg.Sequence - uint16(msg.XORByte)
		base, ok := c.stateBases[channel][baseSeq]
		if ok && base != nil {
			// The RLE-decoded delta length defines the new state size.
			// If the channel shrank, base may be longer — don't carry
			// stale tail bytes forward.
			newLen := len(decoded)
			var reconstructed []byte
			if newLen <= len(c.xorBuf) {
				reconstructed = c.xorBuf[:newLen]
			} else {
				reconstructed = make([]byte, newLen)
			}
			for i := 0; i < newLen; i++ {
				var b byte
				if i < len(base) {
					b = base[i]
				}
				reconstructed[i] = decoded[i] ^ b
			}
			decoded = reconstructed
			// Update the message with reconstructed data so HandleStateChannel
			// gets the fully decoded state, not the raw XOR delta bytes.
			msg.SetData(decoded)
		} else {
			log.Printf("State channel 0x%02X: missing XOR base seq=%d (xorByte=%d), clearing stale bases",
				channel, baseSeq, msg.XORByte)
			// Clear all stale bases for this channel — they're from a broken
			// chain and can't be used. The next keyframe (xor=0) from the
			// server will restart the chain cleanly.
			delete(c.stateBases, channel)
			// Still ACK the message even though we can't decode it. Without
			// the ACK, the server's unacked_messages entry for this channel
			// ages past 60 seconds and triggers terminate(). The data is lost
			// either way (can't XOR-decode without the base), but ACKing
			// keeps the connection alive. The next keyframe (xor=0) will
			// restore correct state.
			c.stateAckMap[channel] = msg.Sequence
			c.stateAckDirtyCount[channel] = 3
			return
		}
	}

	// Store as base for future XOR deltas
	base := make([]byte, len(decoded))
	copy(base, decoded)
	c.stateBases[channel][msg.Sequence] = base

	// Limit stored bases to 32
	if len(c.stateBases[channel]) > 32 {
		var oldest uint16
		for k := range c.stateBases[channel] {
			if oldest == 0 || k < oldest {
				oldest = k
			}
		}
		delete(c.stateBases[channel], oldest)
	}

	// #21: PS2 rejects state channel sequences with diff > CONNECTION_FLUSH_WINDOW (0x1000 = 4096).
	// connection_record_state_channel: if (0x1000 < diff) { goto error_stat; }
	if prevSeq, ok := c.stateAckMap[channel]; ok {
		seqDiff := int16(msg.Sequence - prevSeq)
		if seqDiff > 4096 || seqDiff < -4096 {
			log.Printf("STATE ch=0x%02X seq diff %d (prev=%d new=%d) > 4096, rejecting", channel, seqDiff, prevSeq, msg.Sequence)
			return
		}
	}

	// Update persistent ACK map — always overwrite with latest received sequence.
	// PS2 does: stc.unknown4 = m.seqnum (unconditional overwrite).
	// This is critical for channel reassignment: when Sandstorm moves entity A
	// out of range and assigns entity B to the same channel, sequences reset to 1.
	// If we kept the old high ACK (e.g., seq=15), the server's validation check
	// ("can't ack more than we've sent") rejects the entire packet body, killing
	// all state channel processing including our 0x40 position updates.
	//
	// ACK all valid state channels (0x00-0xF7). Java Sandstorm allocates
	// stateChannels[0xF8] (all 248) and tracks sent messages in unacked_messages.
	// Un-ACK'd state channel messages age past 60 seconds and trigger terminate().
	// Channel 0x42 (stat channel) IS tracked — earlier crash was from stale
	// pre-promotion sequences before we added the promotion state reset.
	c.stateAckMap[channel] = msg.Sequence
	// PS2 uses per-channel dirty counter (set on receive, decremented per flush).
	// Set to 3 so the ACK is sent across 3 consecutive flush cycles, providing
	// redundancy against UDP packet loss. If the single packet carrying a state ACK
	// is lost, the server never gets the ACK and the state message ages past 60s →
	// terminate. Sending across 3 flushes costs only 3 extra bytes per channel per
	// flush for 2 extra cycles. The server handles redundant ACKs idempotently.
	c.stateAckDirtyCount[channel] = 3

	// Update the message's data to the fully decoded version
	msg.SetData(decoded)
}

// processLobbyPacket tracks arrival and reliable sequences for the pre-promotion
// lobby session. Only updates ACK state — lobby messages (pings, time) are not
// delivered to game logic. Must be called with c.mu held.
func (c *Connection) processLobbyPacket(body *protocol.SegmentBody) {
	if c.lobby == nil || body == nil {
		return
	}
	lobby := c.lobby

	// Track arrival number (same PS2 signed-compare logic as main connection)
	arrivalDiff := int16(body.ArrivalNumber - lobby.recvArrival)

	if arrivalDiff > 0 {
		diff := uint16(arrivalDiff)
		if diff > 4096 {
			return // Reject huge jumps
		}
		if diff <= 64 {
			lobby.arrivalHistory = (lobby.arrivalHistory << uint(diff)) | (1 << uint(diff - 1))
		} else {
			lobby.arrivalHistory = 0
		}
		lobby.recvArrival = body.ArrivalNumber
		lobby.arrivalDirty = true
		if lobby.goodArrivalCount < 255 {
			lobby.goodArrivalCount++
		}
	} else if arrivalDiff < 0 {
		diff := uint16(-arrivalDiff)
		if diff <= 64 {
			bit := uint64(1) << uint(diff-1)
			if lobby.arrivalHistory&bit == 0 {
				lobby.arrivalHistory |= bit
				lobby.goodArrivalCount = 0
			}
		}
		// Too old or duplicate — ignore
	}
	// Exact duplicate (arrivalDiff == 0) — ignore

	// Track reliable message sequences for GuaranteedAck
	for _, msg := range body.Messages {
		if msg.Type.IsReliable() && msg.Sequence > 0 {
			if msg.Sequence <= lobby.recvReliable {
				continue // Already in contiguous range
			}
			if lobby.reliableOOO[msg.Sequence] {
				continue // Already received OOO
			}
			if msg.Sequence == lobby.recvReliable+1 {
				lobby.recvReliable = msg.Sequence
				// Drain OOO seqs that are now contiguous
				for lobby.reliableOOO[lobby.recvReliable+1] {
					delete(lobby.reliableOOO, lobby.recvReliable+1)
					lobby.recvReliable++
				}
			} else {
				if lobby.reliableOOO == nil {
					lobby.reliableOOO = make(map[uint16]bool)
				}
				lobby.reliableOOO[msg.Sequence] = true
			}
		}
	}
}

// buildLobbyACKPacket constructs a minimal DRDP packet with FlushAck + GuaranteedAck
// for the lobby session. No messages, no state ACKs — just acknowledgments.
// Must be called with c.mu held.
func (c *Connection) buildLobbyACKPacket() []byte {
	lobby := c.lobby

	buf := make([]byte, 64) // Lobby ACK packets are tiny
	writer := buffer.NewWriter(buf)

	// Write endpoints: our local endpoint → lobby's remote endpoint
	writer.WriteUint16(c.localEndpoint)
	writer.WriteUint16(lobby.remoteEndpoint)

	// Segment header: ClientInitiated only (handshake complete, no IsRemote)
	segFlags := uint64(protocol.SegmentFlagClientInitiated)
	segHeaderPos := writer.Position()
	writer.Write7BitEncodedUInt64(segFlags | 0x7FF) // Placeholder body size

	// Body starts here
	bodyStartPos := writer.Position()

	// Body flags
	var bodyFlags protocol.SegmentBodyFlags
	bodyFlags |= protocol.BodyFlagFlushAck // no 0x04 mask: see BuildPacket
	bodyFlags |= protocol.BodyFlagGuaranteedAck

	guaranteedAckMask := GuaranteedAckMask(lobby.reliableOOO, lobby.recvReliable)
	if guaranteedAckMask != 0 {
		bodyFlags |= protocol.BodyFlagGuaranteedAckMask
	}

	if lobby.goodArrivalCount < 17 {
		bodyFlags |= protocol.BodyFlagRdpMessage // 0x20 short flag
	}

	writer.WriteByte(byte(bodyFlags))

	// Write arrival number (our lobby packet counter)
	writer.WriteUint16(lobby.sendArrival)
	lobby.sendArrival++

	// Write FlushAck (last received lobby arrival)
	writer.WriteUint16(lobby.recvArrival)

	// Write GuaranteedAck (last contiguous reliable seq from lobby)
	writer.WriteUint16(lobby.recvReliable)
	if bodyFlags&protocol.BodyFlagGuaranteedAckMask != 0 {
		writer.Write7BitEncodedUInt64(guaranteedAckMask)
	}

	// Patch segment header with actual body size
	actualBodySize := writer.Position() - bodyStartPos
	val := segFlags | uint64(actualBodySize)
	i := segHeaderPos
	for val >= 0x80 {
		buf[i] = byte(val&0x7F) | 0x80
		val >>= 7
		i++
	}
	buf[i] = byte(val)

	// Append CRC
	data := writer.Bytes()
	result := protocol.AppendCRC(data)

	if lobby.sendArrival <= 5 || lobby.sendArrival%50 == 0 {
		log.Printf("LOBBY ACK: arrival=%d flushAck=%d guaranteedAck=%d bodyFlags=0x%02X (%d bytes)",
			lobby.sendArrival-1, lobby.recvArrival, lobby.recvReliable, byte(bodyFlags), len(result))
	}

	return result
}

// flushLobbyLocked builds a lobby ACK packet if the lobby has pending ACKs.
// Must be called with c.mu held. Returns nil if no lobby work.
func (c *Connection) flushLobbyLocked() []byte {
	if c.lobby == nil || !c.lobby.arrivalDirty {
		return nil
	}
	pkt := c.buildLobbyACKPacket()
	c.lobby.arrivalDirty = false
	return pkt
}

// sendLobbyPacket sends a lobby ACK packet if non-nil.
func (c *Connection) sendLobbyPacket(pkt []byte) {
	if pkt != nil {
		if _, err := c.conn.Write(pkt); err != nil {
			log.Printf("UDP lobby send error: %v", err)
		}
	}
}

// tryAssembleFragments attempts to reassemble buffered fragments in sequence order.
// Must be called with c.mu held. Returns the assembled message if complete, nil if gaps remain.
func (c *Connection) tryAssembleFragments() *protocol.Message {
	if c.fragmentFinalSeq == 0 || len(c.fragmentParts) == 0 {
		return nil
	}

	// Find the lowest sequence in our fragment map
	var minSeq uint16 = c.fragmentFinalSeq
	for seq := range c.fragmentParts {
		if seq < minSeq {
			minSeq = seq
		}
	}

	// Check if all sequences from minSeq to fragmentFinalSeq are present
	expectedCount := int(c.fragmentFinalSeq-minSeq) + 1
	if len(c.fragmentParts) < expectedCount {
		return nil // Still have gaps — wait for more fragments
	}

	// Verify every seq is present (no gaps)
	for seq := minSeq; seq <= c.fragmentFinalSeq; seq++ {
		if _, ok := c.fragmentParts[seq]; !ok {
			return nil // Gap at this seq
		}
	}

	// All present — reassemble in sequence order
	var fullData []byte
	for seq := minSeq; seq <= c.fragmentFinalSeq; seq++ {
		fullData = append(fullData, c.fragmentParts[seq]...)
	}

	finalSeq := c.fragmentFinalSeq

	// Clear fragment state
	c.fragmentParts = nil
	c.fragmentFinalSeq = 0
	c.fragmentStartTime = time.Time{}

	// Parse opcode from first 2 bytes of reassembled data
	var opcode protocol.GameOpcode
	if len(fullData) >= 2 {
		opcode = protocol.GameOpcode(uint16(fullData[0]) | uint16(fullData[1])<<8)
	}

	log.Printf("Fragment assembly: %d parts, seqs %d-%d, %d bytes, opcode=0x%04X",
		expectedCount, minSeq, finalSeq, len(fullData), uint16(opcode))

	finalMsg := &protocol.Message{}
	finalMsg.Type = protocol.MessageTypeReliable
	finalMsg.Sequence = finalSeq
	finalMsg.Opcode = opcode
	finalMsg.SetData(fullData)
	return finalMsg
}

// TimedOut returns true if the DRDP 240-second connection timeout has been exceeded.
// PS2 connection_flush: if (time_last_flush + 240000) - current_time < 1 → assert.
// Offset 0xa4 is updated at end of every successful flush (line 1296).
func (c *Connection) TimedOut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timedOut
}

// Flush sends any pending outgoing messages.
func (c *Connection) Flush() {
	c.mu.Lock()

	// #27: PS2 240-second connection timeout.
	// connection_flush line 1280: if ((time_last_flush + 240000) - current_time < 1) { assert }
	// PS2 offset 0xa4 = time of last successful flush, set at end of every flush (line 1296).
	// Only checked after first flush has occurred (0x200 flag gate, line 1269).
	if !c.lastFlushTime.IsZero() && time.Since(c.lastFlushTime) >= 240*time.Second {
		if !c.timedOut {
			log.Printf("DRDP: connection timeout — 240s since last flush (PS2 assert)")
			c.timedOut = true
		}
		c.mu.Unlock()
		return
	}

	// Server staleness detection: if we've received at least one packet but nothing
	// for 60 seconds, the server has likely crashed or the network is partitioned.
	// 60 seconds matches the server's own unacked_messages timeout threshold.
	if !c.lastRecvTime.IsZero() && time.Since(c.lastRecvTime) > 60*time.Second {
		if !c.timedOut {
			log.Printf("DRDP: server silent for 60s (last recv %v ago), marking stale",
				time.Since(c.lastRecvTime).Round(time.Second))
			c.timedOut = true
		}
		c.mu.Unlock()
		return
	}

	// Fragment cleanup: if a final 0xFB fragment is lost, buffered 0xFA fragments
	// remain forever. Discard orphaned fragments after 30 seconds.
	if len(c.fragmentParts) > 0 && !c.fragmentStartTime.IsZero() &&
		time.Since(c.fragmentStartTime) > 30*time.Second {
		log.Printf("Fragment cleanup: discarding %d orphaned fragments after 30s", len(c.fragmentParts))
		c.fragmentParts = nil
		c.fragmentFinalSeq = 0
		c.fragmentStartTime = time.Time{}
		c.metrics.FragmentsExpired++
	}

	// PS2 keepalive: connection_flush sends a 1-byte 0x12 as type 0xF9
	// (guaranteed) after 30 seconds of no outgoing activity. This prevents
	// the server from timing out idle connections (e.g., during character select).
	if !c.lastGuaranteedSendTime.IsZero() && len(c.outgoing) == 0 &&
		time.Since(c.lastGuaranteedSendTime) >= 30*time.Second {
		// PS2: connection_send_guaranteed checks send window (send_seq - acked_seq < 0x1000).
		// If window full, send fails → connection_send_internal sets conn.flags |= 0x100.
		// connection_flush line 1138: checks 0x100 flag after keepalive → returns -1 (abort flush).
		if c.sendReliable-c.serverAckedReliable >= 0x1000 {
			log.Printf("KEEPALIVE: send window full (send=%d acked=%d), aborting flush (PS2 0x100 flag)",
				c.sendReliable, c.serverAckedReliable)
			c.mu.Unlock()
			return
		}
		keepalive := &protocol.Message{}
		keepalive.Type = protocol.MessageTypePing // 0xF9
		keepalive.SetData([]byte{0x12})
		c.sendReliable++
		keepalive.Sequence = c.sendReliable
		c.outgoing = append(c.outgoing, keepalive)
		log.Printf("KEEPALIVE: sending 0xF9 ping after 30s idle")
	}

	// PS2 per-message retransmit (#26): iterate ALL unacked messages and retransmit
	// any whose individual send timer has expired. PS2 tracks per-message timestamps
	// and retransmits all expired messages in one flush cycle.
	//
	// PS2 also has dual rate-limits (conn+0x1170 / conn+0x1174) selected by
	// conn.flags & 0x40 (instance_ack). Pre-handshake uses conn+0x1170; post-handshake
	// selects between conn+0x1170 and conn+0x1174 based on connection state (sw_sw check).
	// These gate low-priority message deferral in the per-message send loop.
	// Go approximation: RTO inflation during handshake (see end of Flush) provides
	// pre-handshake conservatism. Low-priority deferral not needed since Go state
	// channels are keyframe-only (no retransmit chain to protect).
	//
	// PS2 conn+0x1178: max retransmit age per message. If any message exceeds this
	// threshold without ACK, connection is killed (error code 3 → FUN_004b3e58).
	if len(c.unacked) > 0 {
		now := time.Now()
		// Collect all expired messages first, then prepend in order.
		// Previous code prepended individually, which reversed the order when
		// multiple messages expired in the same flush (seq=3,5 became [5,3,...]).
		var toRetransmit []*protocol.Message
		for _, msg := range c.unacked {
			sendTime, ok := c.unackedSendTime[msg.Sequence]
			if !ok {
				continue
			}
			age := now.Sub(sendTime)
			// PS2 connection_flush: if (conn+0x1178 < current_time - send_time) → error 3.
			// Messages older than 60s without ACK indicate a dead connection.
			if age >= 60*time.Second {
				log.Printf("RETRANSMIT: message seq=%d aged out (%v > 60s), killing connection (PS2 error 3)",
					msg.Sequence, age)
				c.timedOut = true
				c.mu.Unlock()
				return
			}
			if age >= c.rto {
				retransmit := *msg // shallow copy — data slice shared (read-only)
				toRetransmit = append(toRetransmit, &retransmit)
				c.unackedSendTime[msg.Sequence] = now // Reset timer for this message
				c.metrics.Retransmits++
				log.Printf("RETRANSMIT: resending reliable seq=%d type=0x%02X (serverAcked=%d, unacked=%d)",
					msg.Sequence, byte(msg.Type), c.serverAckedReliable, len(c.unacked))
			}
		}
		if len(toRetransmit) > 0 {
			c.outgoing = append(toRetransmit, c.outgoing...)
		}
	}

	// PS2 connection_flush "nothing to send" check (#28):
	// If no pending ACK bits AND no pending messages AND no SessionAck → skip.
	// State ACKs are piggybacked onto packets sent for other reasons (not standalone).
	hasACKs := c.arrivalDirty || c.sessionAckPending
	if len(c.outgoing) == 0 && !hasACKs {
		// Nothing for main connection — but lobby may need flushing
		lobbyPkt := c.flushLobbyLocked()
		c.mu.Unlock()
		c.sendLobbyPacket(lobbyPkt)
		return
	}

	// #26: PS2 100ms ACK timer — when only ACKs are pending (no outgoing messages),
	// delay flush until desired_flush_time (now + 100ms from first arrival).
	// This batches multiple arrivals' ACKs into one packet, reducing overhead.
	// Messages are never delayed — only ACK-only packets wait for the timer.
	if len(c.outgoing) == 0 && hasACKs &&
		!c.ackFlushDeadline.IsZero() && time.Now().Before(c.ackFlushDeadline) {
		// Main ACKs delayed — but flush lobby if needed (don't delay lobby ACKs)
		lobbyPkt := c.flushLobbyLocked()
		c.mu.Unlock()
		c.sendLobbyPacket(lobbyPkt)
		return
	}

	// Take the outgoing messages
	msgs := c.outgoing
	c.outgoing = nil

	// Track NEW reliable messages for retransmit (skip retransmitted ones already in unacked).
	// Use a set for O(1) dedup instead of O(N) linear scan per message.
	unackedSet := make(map[uint16]bool, len(c.unacked))
	for _, u := range c.unacked {
		unackedSet[u.Sequence] = true
	}
	for _, msg := range msgs {
		if msg.Type.IsReliable() && !unackedSet[msg.Sequence] {
			c.unacked = append(c.unacked, msg)
			unackedSet[msg.Sequence] = true
		}
	}
	// Build packet UNDER LOCK — BuildPacket reads/writes conn maps (stateAckDirtyCount,
	// stateAckMap, reliableOOO) and fields (sendArrival++, needsNewInst). Without the lock,
	// concurrent processStateChannel writes cause Go map data races → corruption/panic.
	packet := BuildPacket(c, msgs)

	// Post-build bookkeeping (still under lock)
	now := time.Now()
	sentArrival := c.sendArrival - 1 // BuildPacket incremented after writing
	c.flushSendTimes[sentArrival&0x3FF] = now // #21: store per-seq send timestamp for RTT
	// PS2 connection_flush line 1292: conn.flags &= 0xfff0 — clears bits 0-3 atomically.
	c.arrivalDirty = false
	c.sessionAckPending = false
	c.ackFlushDeadline = time.Time{} // #26: clear 100ms ACK timer after flush
	// PS2 offset 0xa4 (line 1296): store current time as time_last_flush.
	c.lastFlushTime = now
	// PS2 connection_flush line 1262-1267: when conn.flags & 0x40 NOT set
	// (instance_ack not received), inflate min RTT (conn+0x1124) by 1000ms per
	// flush. Since RTO = 2*minRTT, this effectively adds 2000ms to RTO per flush.
	if !c.handshakeComplete {
		c.rto += 2000 * time.Millisecond // PS2: +1000 to minRTT → +2000 to RTO (RTO = 2*minRTT)
		if c.rto > 10*time.Second {
			c.rto = 10 * time.Second
		}
	}
	// #26: Record per-message send timestamps for newly sent reliable messages
	for _, msg := range msgs {
		if msg.Type.IsReliable() {
			if _, exists := c.unackedSendTime[msg.Sequence]; !exists {
				c.unackedSendTime[msg.Sequence] = now
			}
			c.lastGuaranteedSendTime = now
		}
	}

	// Build lobby ACK packet alongside main packet (still under lock)
	lobbyPkt := c.flushLobbyLocked()
	c.mu.Unlock()

	// Send packets (I/O only — no lock needed, no conn state accessed)
	if _, err := c.conn.Write(packet); err != nil {
		log.Printf("UDP send error: %v", err)
	}
	c.sendLobbyPacket(lobbyPkt)
}

// SendInitialPacket sends the initial connection packet to the login endpoint (0xFFFE).
// sessionToken is the 32-byte token from TCP auth (can be nil for SPIRE).
func (c *Connection) SendInitialPacket(gameVersion byte, username string, sessionToken []byte) {
	// Queue DiscVersion (opcode 0x0000) - reliable
	// PS2 always sends 0x12 as uint32 LE (4 bytes), regardless of Frontiers/Vanilla
	discBuf := make([]byte, 4)
	discBuf[0] = protocol.GameVersionEQOAVanilla // PS2 sends 0x12
	c.QueueReliable(protocol.OpcodeDiscVersion, discBuf)

	// Queue Authenticate (opcode 0x0904) - reliable
	// PS2 wire format (from pcap):
	//   [0]     version (0x00)
	//   [1-4]   unknown uint32 (3)
	//   [5-8]   unknown uint32 (4) — possibly "EQOA" length
	//   [9-12]  "EQOA" marker
	//   [13-16] username length (uint32 LE)
	//   [17+N]  username
	//   [17+N]  has-token flag (0x01)
	//   [18+N]  32-byte session token
	authBuf := make([]byte, 256)
	w := buffer.NewWriter(authBuf)
	w.WriteByte(0x00)                    // Version byte (PS2 sends 0x00)
	w.WriteUint32(3)                     // Unknown field (always 3 in pcap)
	w.WriteUint32(4)                     // Unknown field (always 4 in pcap)
	w.WriteBytes([]byte("EQOA"))         // Marker
	w.WriteUint32(uint32(len(username))) // Username length
	w.WriteStringUTF8Raw(username)       // Username
	if len(sessionToken) == 32 {
		w.WriteByte(0x01)            // Has-token flag
		w.WriteBytes(sessionToken)   // 32-byte session token
	}
	authData := authBuf[:w.Position()]
	log.Printf("Authenticate payload (%d bytes): %X", len(authData), authData)
	c.QueueReliable(protocol.OpcodeAuthenticate, authData)

	c.Flush()
}

// SendGameAuth sends authentication to the game server endpoint (0xB991).
// sessionToken is the 32-byte token from TCP auth (can be nil for SPIRE).
func (c *Connection) SendGameAuth(gameVersion byte, username string, sessionToken []byte) {
	// Queue DiscVersion (opcode 0x0000) - 4 bytes like PS2
	discBuf := make([]byte, 4)
	discBuf[0] = protocol.GameVersionEQOAVanilla // PS2 sends 0x12
	c.QueueReliable(protocol.OpcodeDiscVersion, discBuf)

	// Queue Authenticate2 (opcode 0x0001 on game server)
	// Same format as Authenticate (0x0904)
	authBuf := make([]byte, 256)
	w := buffer.NewWriter(authBuf)
	w.WriteByte(0x00)                    // Version byte
	w.WriteUint32(3)                     // Unknown field
	w.WriteUint32(4)                     // Unknown field
	w.WriteBytes([]byte("EQOA"))         // Marker
	w.WriteUint32(uint32(len(username))) // Username length
	w.WriteStringUTF8Raw(username)       // Username
	if len(sessionToken) == 32 {
		w.WriteByte(0x01)            // Has-token flag
		w.WriteBytes(sessionToken)   // 32-byte session token
	}
	authData := authBuf[:w.Position()]
	log.Printf("Authenticate2 payload (%d bytes): %X", len(authData), authData)
	c.QueueReliable(protocol.OpcodeAuthenticate2, authData)

	c.Flush()
}

// SendReset sends a Reset packet to close the current connection before switching endpoints.
// PS2 sends this to the server's actual endpoint before connecting to the game server.
func (c *Connection) SendReset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	buf := make([]byte, 64)
	writer := buffer.NewWriter(buf)

	// Write endpoints: use server's actual endpoint as remote
	writer.WriteUint16(c.localEndpoint)
	if c.serverEndpoint != 0 {
		writer.WriteUint16(c.serverEndpoint)
	} else {
		writer.WriteUint16(c.remoteEndpoint)
	}

	// Segment flags: HasInstance | Reset | 0x40000 | ClientInit
	segFlags := uint64(protocol.SegmentFlagHasInstance) |
		uint64(protocol.SegmentFlagResetConnection) |
		uint64(protocol.SegmentFlagData0x40000) |
		uint64(protocol.SegmentFlagClientInitiated)
	// Body size = 0 for reset
	writer.Write7BitEncodedUInt64(segFlags)

	// Write instance ID
	writer.WriteUint32(c.instanceID)

	// DRDP Reset includes the instance ID again as extra uint32
	writer.WriteUint32(c.instanceID)

	data := writer.Bytes()
	data = protocol.AppendCRC(data)
	c.conn.Write(data)
	log.Printf("Sent Reset packet to endpoint 0x%04X", c.serverEndpoint)
}

// ClearStateAck removes a single channel from the persistent ACK map and
// clears its XOR delta bases. Called when a channel is emptied (objectID=0)
// so we don't send stale ACKs for a channel the server has freed, and so
// the next entity assigned to this channel starts with a fresh XOR chain.
func (c *Connection) ClearStateAck(channel byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.stateAckMap, channel)
	delete(c.stateAckDirtyCount, channel)
	delete(c.stateBases, channel)
}

// SwitchToGameServer reconfigures the connection for the game server endpoint.
func (c *Connection) SwitchToGameServer(endpointID uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remoteEndpoint = endpointID
	// Generate a NEW instance ID for the game server connection.
	// Sandstorm creates a new Connection on the game endpoint, so a new instance
	// is expected (NewInstance flag signals this). Critically, the new instance ID
	// lets us reject stale login server retransmissions — old packets carry the
	// previous instance ID and are dropped by the instance check in ProcessIncoming.
	c.instanceCounter++
	c.instanceID = uint32(c.instanceCounter)<<16 | uint32(c.localEndpoint)
	c.needsNewInst = true
	c.hasInstance = true
	// Reset sequences
	c.sendArrival = 1 // PS2/Java start flush_seq at 1 (arrival 0 rejected as dup by Sandstorm)
	c.sendReliable = 0
	c.recvArrival = 0xFFFF // Sentinel: no packets received yet (#22)
	c.recvReliable = 0
	c.reliableOOO = make(map[uint16]bool)
	c.serverAckedReliable = 0
	c.lastSentReliable = 0
	c.serverEndpoint = 0 // Reset — learn from first game server response (Sandstorm reuses endpoint pool, so game server may respond from a different endpoint than 0xB991)
	c.serverEndpointPacked = 0  // Reset — learned from server's DidServerInitiate local_endpoint
	c.instanceRemote = 0        // Reset — learned from server's NewInstance or SessionAck
	c.gotSessionAck = false
	c.handshakeComplete = false // Reset for new handshake
	c.handshakeSentCount = 0   // Reset handshake packet counter
	c.goodArrivalCount = 0     // Reset for new session (PS2 short flag 0x20)
	c.arrivalHistory = 0       // Reset arrival bitmap
	c.arrivalDirty = false
	c.sessionAckPending = false
	c.serverShort = false
	c.ackFlushDeadline = time.Time{}
	c.timedOut = false
	c.lastFlushTime = time.Time{} // Reset for new session (PS2 0x200 flag cleared)
	c.rttSamples = [16]time.Duration{}
	c.rttSampleIdx = 0
	c.rto = 2 * time.Second
	c.lastGuaranteedSendTime = time.Time{}
	c.flushSendTimes = [1024]time.Time{}
	c.lastSentReliable = 0
	c.didServerInit = false // Reset for game server session detection
	c.pktCount = 0         // Reset debug counter for game server phase
	c.unacked = nil
	c.unackedSendTime = make(map[uint16]time.Time)
	c.fragmentParts = nil
	c.fragmentFinalSeq = 0
	c.fragmentStartTime = time.Time{}
	c.outgoing = nil
	c.stateAckMap = make(map[byte]uint16)
	c.stateAckDirtyCount = make(map[byte]byte)
}

// Metrics returns a snapshot of protocol health counters.
func (c *Connection) Metrics() ConnectionMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metrics
}

// updateRTT updates the retransmit timeout using PS2's 16-sample max-RTT algorithm.
// PS2 connection_add_rtt_sample: circular buffer uint16[16] at conn+0x1126,
// index at conn+0x1122 (& 0x0F). Computes MAXIMUM across all 16 samples (conn+0x1124).
// PS2 uses max RTT for conservative RTO — avoids premature retransmits under jitter.
// RTO = 2 * maxRTT (clamped to 200ms..10s).
func (c *Connection) updateRTT(rtt time.Duration) {
	c.rttSamples[c.rttSampleIdx&0x0F] = rtt
	c.rttSampleIdx++

	// PS2: find MAXIMUM across all 16 samples (conservative retransmit).
	// connection_add_rtt_sample seeds from history[0], then for each entry:
	//   if (entry < current) keep current; else use entry → accumulates max.
	maxRTT := time.Duration(0)
	for _, s := range c.rttSamples {
		if s > maxRTT {
			maxRTT = s
		}
	}

	if maxRTT > 0 {
		c.rto = maxRTT * 2
	}
	if c.rto < 200*time.Millisecond {
		c.rto = 200 * time.Millisecond
	}
	if c.rto > 10*time.Second {
		c.rto = 10 * time.Second
	}
}
