package network

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
	"github.com/eqoa/client/internal/protocol"
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
	hasInstance     bool
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

	// Guaranteed messages held out of order (PS2 m_delayed), keyed by seq.
	// connection_move_delayed_to_arrivals releases them strictly in sequence:
	// recvReliable+1 first, then on while the next one is here. The ack mask
	// is built from these keys.
	delayed map[uint16]*protocol.Message

	// Fragment pieces (0xFA) already released in sequence (PS2 m_arrivals):
	// they accumulate until the next 0xFB/0xF9 is released, and the whole run
	// is delivered as one message.
	arrivals []*protocol.Message

	// Server's ack of our reliable messages (PS2 m_usTxAck)
	serverAckedReliable uint16

	// Highest flush number the server has acked (PS2 m_usFlushAck). An
	// outstanding message sent in an EARLIER flush is then known lost.
	flushAckMax uint16

	// Last guaranteed ack we sent (to avoid resending stale values)
	lastSentReliable uint16

	// PS2 arrival history bitmap: 64-bit sliding window tracking which of the
	// last 64 arrivals were received. Used for FlushAck mask (body flag 0x04).
	// PS2 connection_record_segment: on new arrival: arrivalHistory = (arrivalHistory << 1) | 1
	// On gap: arrivalHistory <<= gapSize. On old arrival: set bit at position.
	arrivalHistory uint64

	// Per state channel: the highest sequence taken (PS2 state_channel.rx,
	// 0 until the first) and how many more flushes ack it (m_ucRxFlag, set
	// to 2 by every message on the channel, stale ones included). rx only
	// moves forward, so the ack never goes backwards.
	stateRx     map[byte]uint16
	stateRxFlag map[byte]byte

	// Outgoing message queue
	outgoing []*protocol.Message

	// Unacked reliable messages (for retransmit)
	unacked []*protocol.Message

	// Per-message send timestamps for PS2-accurate retransmit (#26).
	// PS2 tracks per-message timers; retransmits ALL expired messages per flush.
	unackedSendTime map[uint16]time.Time

	// The flush number each unacked message last went out in (message_set_flush).
	unackedFlush map[uint16]uint16

	// Incoming parsed packets channel
	Incoming chan *ReceivedPacket

	// XOR delta bases for state channels (channel -> seq -> decoded data),
	// PS2 state_channel.m_rxList
	stateBases map[byte]map[uint16][]byte

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
	rto            time.Duration     // Retransmit timeout: max RTT sample + 100 ms (initial 1,100 ms)

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

	// The two notices the DRDP library hands the application itself rather
	// than off the wire. connectPending: a session the server opened has
	// begun (MESSAGE_TYPE_CONNECT, 0xFF, queued ahead of its first segment's
	// messages). terminatePending/terminateReason: the connection has ended
	// (MESSAGE_TYPE_DISCONNECT, 0xFD, connection_terminate_reason's byte).
	connectPending   bool
	terminatePending bool
	terminateReason  byte

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
		conn:            udpConn,
		addr:            addr,
		localEndpoint:   localEndpoint,
		remoteEndpoint:  remoteEndpoint,
		instanceID:      instanceID,
		instanceCounter: 2, // PS2 starts at 2
		hasInstance:     true,
		needsNewInst:    true,
		sendArrival:     1,      // PS2/Java start flush_seq at 1, not 0 (arrival 0 is rejected as dup by Sandstorm)
		recvArrival:     0xFFFF, // Sentinel: no packets received yet (#22 signed compare)
		rto:             initialRTO,
		Incoming:        make(chan *ReceivedPacket, 2048),
		delayed:         make(map[uint16]*protocol.Message),
		unackedSendTime: make(map[uint16]time.Time),
		unackedFlush:    make(map[uint16]uint16),
		stateRx:         make(map[byte]uint16),
		stateRxFlag:     make(map[byte]byte),
		stateBases:      make(map[byte]map[uint16][]byte),
		sendStateSeq:    make(map[byte]uint16),
		msgTypeCounts:   make(map[byte]int),
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

// The DRDP size limits (connection.cpp): a guaranteed message is cut into
// 1,300-byte pieces, and a best-effort or state message may be no larger.
const (
	maxMessagePiece = 1300
	initialRTO      = 1100 * time.Millisecond           // m_usTimeout 1000, + 100 per message
	maxRTO          = (0xFFFF + 100) * time.Millisecond // m_usTimeout is a uns16
)

// QueueReliable queues a reliable message with the given opcode and data.
// Returns false if the send window is full (4096 in-flight limit).
//
// connection_send_guaranteed: a message over 1,300 bytes goes out as 0xFA
// pieces of 1,300 and a final 0xFB, each with its own sequence number, and
// either every piece fits the 4,096 window or none is queued.
func (c *Connection) QueueReliable(opcode protocol.GameOpcode, data []byte) bool {
	// Build message data: opcode (2 bytes) + payload
	msgData := make([]byte, 2+len(data))
	msgData[0] = byte(opcode)
	msgData[1] = byte(opcode >> 8)
	copy(msgData[2:], data)
	pieces := (len(msgData) + maxMessagePiece - 1) / maxMessagePiece

	c.mu.Lock()
	// PS2: fails when a piece's seqnum would pass the last ack + 4096.
	if int16(c.sendReliable+uint16(pieces)-(c.serverAckedReliable+0x1000)) > 0 {
		log.Printf("QueueReliable: send window full (send=%d acked=%d), dropping opcode=0x%04X (PS2 returns -1)",
			c.sendReliable, c.serverAckedReliable, uint16(opcode))
		c.mu.Unlock()
		return false
	}
	for off := 0; off == 0 || off < len(msgData); off += maxMessagePiece {
		end := min(off+maxMessagePiece, len(msgData))
		msg := &protocol.Message{}
		msg.Type = protocol.MessageTypeReliable
		if end < len(msgData) {
			msg.Type = protocol.MessageTypeSegmentReliable
		}
		msg.Opcode = opcode
		msg.SetData(msgData[off:end])
		c.sendReliable++
		msg.Sequence = c.sendReliable
		c.outgoing = append(c.outgoing, msg)
	}
	log.Printf("QueueReliable: opcode=0x%04X seq=%d dataLen=%d pieces=%d", uint16(opcode), c.sendReliable, len(data), pieces)
	c.mu.Unlock()
	return true
}

// QueueUnreliable queues an unreliable message. One over 1,300 bytes is
// refused, as connection_send_best_effort refuses it: it could never fit a
// packet, and would block the queue behind it.
func (c *Connection) QueueUnreliable(opcode protocol.GameOpcode, data []byte) {
	if 2+len(data) > maxMessagePiece {
		log.Printf("QueueUnreliable: opcode=0x%04X is %d bytes, over the %d limit; dropped",
			uint16(opcode), 2+len(data), maxMessagePiece)
		return
	}
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

	// message_get_src: every message is from the segment's sender, whose
	// endpoint id a session the server opened carries in its header.
	for _, m := range body.Messages {
		m.Src = pkt.SessionID
	}

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
				held := make(map[uint16]bool, len(c.delayed))
				for seq := range c.delayed {
					held[seq] = true
				}
				c.lobby = &LobbySession{
					remoteEndpoint:    c.serverEndpoint,
					instanceID:        c.instanceID,
					recvArrival:       c.recvArrival,
					recvReliable:      c.recvReliable,
					reliableOOO:       held,
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
			c.arrivals = nil // Clear any partial fragments from pre-session
			// connection_parse_segment_body: the first segment of a connection
			// the far end opened carries MESSAGE_TYPE_CONNECT ahead of its own.
			c.connectPending = true
			// Reset ALL reliable tracking for the new promoted session.
			// The server creates a new Connection with fresh counters.
			// Both incoming (recvReliable) and outgoing (sendReliable) must reset.
			log.Printf("ProcessIncoming: resetting reliable tracking (was recv=%d sent=%d sendArrival=%d)",
				c.recvReliable, c.sendReliable, c.sendArrival)
			c.recvReliable = 0
			c.delayed = make(map[uint16]*protocol.Message)
			c.lastSentReliable = 0
			c.goodArrivalCount = 0      // Reset for new session (PS2 resets on promotion)
			c.arrivalHistory = 0        // Reset arrival bitmap for new session
			c.handshakeComplete = false // New session needs fresh handshake
			c.handshakeSentCount = 0    // Reset handshake packet counter
			c.recvArrival = 0xFFFF      // Sentinel: no packets received yet (#22)
			c.arrivalDirty = false
			c.sessionAckPending = false
			c.serverShort = false
			c.ackFlushDeadline = time.Time{}
			c.timedOut = false
			c.terminatePending = false
			c.lastFlushTime = time.Time{} // Reset for new session (PS2 0x200 flag cleared)
			c.rttSamples = [16]time.Duration{}
			c.rttSampleIdx = 0
			c.rto = initialRTO
			c.lastGuaranteedSendTime = time.Time{}
			c.flushSendTimes = [1024]time.Time{}
			c.sendReliable = 0 // Reset outgoing reliable seq for promoted session
			c.sendArrival = 1  // Reset outgoing arrival for promoted session (starts at 1, not 0)
			c.serverAckedReliable = 0
			c.flushAckMax = 0
			c.unacked = nil
			c.unackedSendTime = make(map[uint16]time.Time)
			c.unackedFlush = make(map[uint16]uint16)
			c.outgoing = nil
			// Reset state channel tracking for promoted session.
			// The server creates a new Connection with fresh state channel counters.
			// Stale ACKs from pre-promotion would reference sequences the promoted
			// connection never sent, triggering "can't ack more than we've sent".
			c.stateRx = make(map[byte]uint16)
			c.stateRxFlag = make(map[byte]byte)
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
			c.gotSessionAck = true     // Persistent: we've seen a SessionAck
			c.sessionAckPending = true // Per-flush: cleared after sending (#27)
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

	// connection_parse_segment_body checks the WHOLE segment before
	// connection_record_segment applies any of it, so a segment that fails
	// changes nothing: no arrival recorded, no ack sent for it, no message
	// delivered. The arrival is checked first, with the PS2's signed 16-bit
	// compare (#22): an exact duplicate, an old one already in the bitmap,
	// one more than 64 back, or a jump past CONNECTION_FLUSH_WINDOW (#19).
	arrivalDiff := int16(body.ArrivalNumber - c.recvArrival)
	arrivalValid := arrivalDiff > 0 // PS2 m_iArrival: a new (forward) arrival
	isRejected := false
	switch {
	case arrivalDiff > 4096:
		isRejected = true
	case arrivalDiff == 0:
		isRejected = true
	case arrivalDiff < 0:
		back := -int(arrivalDiff)
		isRejected = back > 64 || c.arrivalHistory&(uint64(1)<<uint(back-1)) != 0
	}
	// Then its messages, against the state of the connection BEFORE this
	// segment: a guaranteed seqnum beyond the window, or a state delta whose
	// reference is gone ("refnum does not exist"), fails the segment.
	var stateStale []bool
	if !isRejected {
		var ok bool
		if stateStale, ok = c.parseSegmentMessages(body); !ok {
			isRejected = true
		}
	}
	if isRejected {
		c.mu.Unlock()
		return nil
	}

	// connection_record_segment: the arrival history is a sliding bitmap of
	// the last 64 arrivals; bit N is arrival (recvArrival - 1 - N).
	if arrivalValid {
		diff := uint16(arrivalDiff)
		if diff > 1 {
			c.metrics.ArrivalGaps++
		}
		if diff <= 64 {
			c.arrivalHistory = (c.arrivalHistory << uint(diff)) | (1 << uint(diff-1))
		} else {
			c.arrivalHistory = 0
		}
		c.recvArrival = body.ArrivalNumber
		if c.goodArrivalCount < 255 {
			c.goodArrivalCount++
		}
	} else {
		// Out of order: mark it, and the run of good arrivals restarts.
		c.arrivalHistory |= uint64(1) << uint(-int(arrivalDiff)-1)
		c.goodArrivalCount = 0
	}
	// What this segment obliges us to acknowledge. Every new arrival is
	// acked (the Go client always has; the PS2 acks a segment only for the
	// messages in it), and so is an old one that carried guaranteed or state
	// messages (connection_record_segment_messages sets conn.flags 3 / 5).
	hasAckable := false
	for _, msg := range body.Messages {
		if msg.Type.IsReliable() || msg.Type.IsStateChannel() {
			hasAckable = true
			break
		}
	}
	if arrivalValid || hasAckable {
		// PS2 100ms ACK timer (#26): when bits 0-3 transition from all-zero,
		// set desired_flush_time = now + 100ms. Batches ACKs within the window.
		if !c.arrivalDirty && !c.sessionAckPending {
			c.ackFlushDeadline = time.Now().Add(100 * time.Millisecond)
		}
		c.arrivalDirty = true // #18: flag for FlushAck/GuaranteedAck (PS2 conn.flags bit 0)
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
		// #23: a FlushAck at or past our NEXT flush number acks a segment we
		// have not sent (PS2: flush_ack >= connection->flush_tx).
		if int16(body.FlushAck-c.sendArrival) >= 0 {
			log.Printf("WARNING: server FlushAck %d >= our next flush %d, ignoring", body.FlushAck, c.sendArrival)
		} else {
			// connection_record_acks: an RTT sample for the flush acked and
			// for each earlier one its mask says was also seen.
			c.recordFlushAck(body.FlushAck)
			if body.Flags&protocol.BodyFlagUnknownPacked != 0 {
				flush := body.FlushAck
				for mask := body.FlushAckMask; mask != 0; mask >>= 1 {
					flush--
					if mask&1 != 0 {
						c.recordFlushAck(flush)
					}
				}
			}
			if int16(body.FlushAck-c.flushAckMax) > 0 {
				c.flushAckMax = body.FlushAck
			}
		}
	}

	// Process server's guaranteed ack of our reliable messages
	// #24: PS2 only processes GuaranteedAck for new (forward) arrivals.
	if arrivalValid && body.Flags&protocol.BodyFlagGuaranteedAck != 0 {
		ack := body.GuaranteedAck
		ahead := int16(ack - c.serverAckedReliable)
		if int16(ack-c.sendReliable) > 0 {
			// #23: PS2 guaranteed_ack >= guaranteed_tx (the NEXT seqnum).
			log.Printf("WARNING: server GuaranteedAck %d > our sendReliable %d, ignoring", ack, c.sendReliable)
		} else if ahead > 4096 {
			// #20: PS2 rejects GuaranteedAck window > CONNECTION_FLUSH_WINDOW (0x1000 = 4096).
			// connection_record_acks: if (window > 0x1000) { log_parse_failure("too many guaranteed rx acks"); }
			log.Printf("WARNING: GuaranteedAck window %d > 4096, ignoring", ahead)
		} else if ahead >= 0 {
			// An ack behind the last one is stale and ignored (PS2: guaranteed_ack
			// < connection->guaranteed_ack clears m_iGuaranteedAck).
			var mask uint64
			if body.Flags&protocol.BodyFlagGuaranteedAckMask != 0 {
				mask = body.GuaranteedAckMask
			}
			c.retireGuaranteed(ack, mask)
			c.serverAckedReliable = ack
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

	// connection_record_segment_messages, in body order.
	var messages []*protocol.Message
	for i, msg := range body.Messages {
		// Debug: log all message types for first 30 arrivals after promotion
		if c.didServerInit && body.ArrivalNumber <= 30 {
			log.Printf("MSG arrival=%d type=0x%02X seq=%d xor=%d size=%d",
				body.ArrivalNumber, byte(msg.Type), msg.Sequence, msg.XORByte, msg.Size())
		}

		switch {
		case msg.Type.IsReliable():
			messages = c.recordGuaranteed(msg, messages)
		case msg.Type.IsStateChannel():
			if c.recordStateMessage(msg, stateStale[i]) {
				messages = append(messages, msg)
			}
		default:
			// #25: a best-effort (0xFC) message is delivered only from a new
			// arrival; an old one's is stale (PS2 buffer_skip).
			if arrivalValid {
				messages = append(messages, msg)
			}
		}
	}
	if c.connectPending {
		c.connectPending = false
		connect := &protocol.Message{Type: protocol.MessageTypeConnect, Src: pkt.SessionID}
		messages = append([]*protocol.Message{connect}, messages...)
	}

	c.mu.Unlock()
	return messages
}

// parseSegmentMessages is the message half of connection_parse_segment_body:
// it fails the segment for a guaranteed seqnum more than 4,096 past
// recvReliable, or for a state message more than 4,096 ahead of its channel
// or whose delta reference is gone. It also XOR-decodes each state delta
// against the reference it names -- the base set as it stood before this
// segment, which is what the PS2 decodes against -- and reports which state
// messages are stale (seq <= the channel's rx): those are dropped, and their
// channel acked again. Must be called with c.mu held.
func (c *Connection) parseSegmentMessages(body *protocol.SegmentBody) (stale []bool, ok bool) {
	stale = make([]bool, len(body.Messages))
	for i, msg := range body.Messages {
		switch {
		case msg.Type.IsReliable():
			if int16(msg.Sequence-(c.recvReliable+4096)) > 0 {
				log.Printf("DRDP: seqnum %d > guaranteed_rx %d + 4096, segment dropped", msg.Sequence, c.recvReliable)
				return nil, false
			}
		case msg.Type.IsStateChannel():
			ch := byte(msg.Type)
			ahead := int16(msg.Sequence - c.stateRx[ch])
			if ahead <= 0 {
				stale[i] = true
				continue
			}
			if ahead > 4096 {
				log.Printf("STATE ch=0x%02X seq %d is %d past rx %d, segment dropped", ch, msg.Sequence, ahead, c.stateRx[ch])
				return nil, false
			}
			if msg.XORByte == 0 {
				continue
			}
			base, found := c.stateBases[ch][msg.Sequence-uint16(msg.XORByte)]
			if !found {
				log.Printf("STATE ch=0x%02X seq %d: reference %d does not exist, segment dropped",
					ch, msg.Sequence, msg.Sequence-uint16(msg.XORByte))
				return nil, false
			}
			data := msg.Data()
			for j := 0; j < len(data) && j < len(base); j++ {
				data[j] ^= base[j]
			}
		}
	}
	return stale, true
}

// recordGuaranteed is connection_record_message for the three guaranteed
// types (0xF9 system, 0xFA fragment, 0xFB guaranteed) plus
// connection_has_reliable before it. A message is held until every one
// before it has arrived, then released in sequence; 0xFA pieces collect until
// the next 0xFB/0xF9 is released and the whole run is delivered as ONE
// message, with the type of its last piece and the seqnum of its first. So a
// fragmented MemoryDump survives losing any of its pieces, and a message that
// overtook a lost piece waits for it. Must be called with c.mu held.
func (c *Connection) recordGuaranteed(msg *protocol.Message, out []*protocol.Message) []*protocol.Message {
	if int16(msg.Sequence-c.recvReliable) <= 0 {
		return out // already released
	}
	if _, held := c.delayed[msg.Sequence]; held {
		return out // a retransmission of one we are holding
	}
	c.delayed[msg.Sequence] = msg
	for {
		next, ok := c.delayed[c.recvReliable+1]
		if !ok {
			break
		}
		delete(c.delayed, c.recvReliable+1)
		c.recvReliable++
		switch {
		case next.Type == protocol.MessageTypeSegmentReliable:
			c.arrivals = append(c.arrivals, next)
		case len(c.arrivals) == 0:
			out = append(out, next)
		default:
			out = append(out, c.joinArrivals(next))
		}
	}
	return out
}

// joinArrivals concatenates the held 0xFA pieces and the piece that ends
// them into one message, as connection_move_delayed_to_arrivals does.
func (c *Connection) joinArrivals(last *protocol.Message) *protocol.Message {
	pieces := append(c.arrivals, last)
	c.arrivals = nil
	total := 0
	for _, p := range pieces {
		total += p.Size()
	}
	data := make([]byte, 0, total)
	for _, p := range pieces {
		data = append(data, p.Data()...)
	}
	msg := &protocol.Message{Type: last.Type, Sequence: pieces[0].Sequence, Src: last.Src}
	if len(data) >= 2 && last.Type != protocol.MessageTypePing {
		msg.Opcode = protocol.GameOpcode(uint16(data[0]) | uint16(data[1])<<8)
	}
	msg.SetData(data)
	log.Printf("Fragment assembly: %d pieces, seqs %d-%d, %d bytes, opcode=0x%04X",
		len(pieces), pieces[0].Sequence, last.Sequence, len(data), uint16(msg.Opcode))
	return msg
}

// recordStateMessage is connection_record_state_message: the message's
// sequence becomes the channel's rx, the channel is acked on the next two
// flushes, and the decoded state is kept as a base for later deltas. The
// bases kept are those from this delta's reference on, or the last 32 after a
// keyframe (message_seqnum_remove_thru, wrap-safe). A stale message (seq <=
// rx, flagged by parseSegmentMessages) is dropped but its channel acked again
// -- with rx, so the ack never goes backwards. Returns whether to deliver it.
// Must be called with c.mu held.
//
// Sandstorm does NOT restart a channel's sequence when it hands the channel
// to another entity: across four PS2 captures, 114 channel reassignments
// carried on the old sequence with a delta against the old entity's state.
func (c *Connection) recordStateMessage(msg *protocol.Message, stale bool) bool {
	ch := byte(msg.Type)
	c.stateRxFlag[ch] = 2
	if stale {
		return false
	}
	c.stateRx[ch] = msg.Sequence

	bases := c.stateBases[ch]
	if bases == nil {
		bases = make(map[uint16][]byte)
		c.stateBases[ch] = bases
	}
	keep := msg.Sequence - 32
	if msg.XORByte != 0 {
		keep = msg.Sequence - uint16(msg.XORByte)
	}
	for seq := range bases {
		if int16(seq-keep) < 0 {
			delete(bases, seq)
		}
	}
	base := make([]byte, msg.Size())
	copy(base, msg.Data())
	bases[msg.Sequence] = base
	return true
}

// recordFlushAck is connection_record_flush_ack: the time the acked flush
// went out is still in the ring, so the difference is a round trip. A flush
// more than 1,024 back has been overwritten. Must be called with c.mu held.
func (c *Connection) recordFlushAck(flush uint16) {
	if int16(flush-(c.sendArrival-1024)) < 0 {
		return
	}
	sendTime := c.flushSendTimes[flush&0x3FF]
	if !sendTime.IsZero() {
		c.updateRTT(time.Since(sendTime))
		c.flushSendTimes[flush&0x3FF] = time.Time{}
	}
}

// retireGuaranteed is connection_record_guaranteed_ack: everything up to and
// including ack is retired, and mask bit N retires ack+2+N (ack+1 is never in
// the mask: it would not be held). Must be called with c.mu held.
func (c *Connection) retireGuaranteed(ack uint16, mask uint64) {
	var remaining []*protocol.Message
	for _, msg := range c.unacked {
		ahead := int(int16(msg.Sequence - ack))
		if ahead <= 0 || (ahead >= 2 && ahead-2 < 64 && mask>>uint(ahead-2)&1 != 0) {
			delete(c.unackedSendTime, msg.Sequence)
			delete(c.unackedFlush, msg.Sequence)
			continue
		}
		remaining = append(remaining, msg)
	}
	c.unacked = remaining
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
			lobby.arrivalHistory = (lobby.arrivalHistory << uint(diff)) | (1 << uint(diff-1))
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

	// Track reliable message sequences for GuaranteedAck (wrap-safe: seq 0
	// follows 65535 like any other)
	for _, msg := range body.Messages {
		if msg.Type.IsReliable() {
			if int16(msg.Sequence-lobby.recvReliable) <= 0 {
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

// TimedOut returns true if the DRDP 240-second connection timeout has been exceeded.
// PS2 connection_flush: if (time_last_flush + 240000) - current_time < 1 → assert.
// Offset 0xa4 is updated at end of every successful flush (line 1296).
func (c *Connection) TimedOut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timedOut
}

// terminateLocked ends the connection, once: connection_terminate_reason
// latches, and the reason byte goes to the application in the notice
// TakeTerminate returns. Must be called with c.mu held.
func (c *Connection) terminateLocked(reason byte) {
	if c.timedOut {
		return
	}
	c.timedOut = true
	c.terminatePending = true
	c.terminateReason = reason
}

// TakeTerminate returns the connection's MESSAGE_TYPE_DISCONNECT (0xFD), one
// reason byte, the first time it is asked after the connection has ended,
// and nil otherwise. CLIENT's pump turns it into Disconnect.
func (c *Connection) TakeTerminate() *protocol.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.terminatePending {
		return nil
	}
	c.terminatePending = false
	m := &protocol.Message{Type: protocol.MessageTypeDisconnect}
	m.SetData([]byte{c.terminateReason})
	return m
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
			c.terminateLocked(10)
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
			c.terminateLocked(2) // connection.cpp: outstanding and silent past the warn time
		}
		c.mu.Unlock()
		return
	}

	// (No orphan-fragment purge: pieces are only ever released in sequence,
	// so held 0xFA pieces are the head of a message whose tail the server is
	// still retransmitting. Dropping them would deliver the tail alone.)

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
		// A copy still queued from an earlier flush (one that did not fit)
		// is not selected again.
		queued := make(map[uint16]bool)
		for _, msg := range c.outgoing {
			if msg.Type.IsReliable() {
				queued[msg.Sequence] = true
			}
		}
		// Collect all expired messages first, then prepend in order.
		// Previous code prepended individually, which reversed the order when
		// multiple messages expired in the same flush (seq=3,5 became [5,3,...]).
		var toRetransmit []*protocol.Message
		for _, msg := range c.unacked {
			sendTime, ok := c.unackedSendTime[msg.Sequence]
			if !ok || queued[msg.Sequence] {
				continue
			}
			age := now.Sub(sendTime)
			// PS2 connection_flush: if (conn+0x1178 < current_time - send_time) → error 3.
			// Messages older than 60s without ACK indicate a dead connection.
			if age >= 60*time.Second {
				log.Printf("RETRANSMIT: message seq=%d aged out (%v > 60s), killing connection (PS2 error 3)",
					msg.Sequence, age)
				c.terminateLocked(3) // connection.cpp: a message older than the fail time
				c.mu.Unlock()
				return
			}
			// connection_message_get_desired_flush_time: due at send time +
			// the RTO (max RTT + 100 ms); or at once when the server has acked
			// a LATER flush than the one this went out in -- the segment that
			// carried it was lost -- provided the server reports a clean run
			// of arrivals (no 0x20 "short" flag, PS2 conn.flags 0x10).
			lost := !c.serverShort && int16(c.flushAckMax-c.unackedFlush[msg.Sequence]) > 0
			if age >= c.rto || lost {
				retransmit := *msg // shallow copy — data slice shared (read-only)
				toRetransmit = append(toRetransmit, &retransmit)
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

	// Build packet UNDER LOCK — BuildPacket reads/writes conn maps (stateRxFlag,
	// stateRx, delayed) and fields (sendArrival++, needsNewInst). Without the lock,
	// concurrent ProcessIncoming writes cause Go map data races → corruption/panic.
	// Messages go in, in order, while they fit (connection_append stops at the
	// first that does not); the rest stay queued, ahead of anything newer.
	packet, written := BuildPacket(c, msgs)
	if written < len(msgs) {
		c.outgoing = append(msgs[written:len(msgs):len(msgs)], c.outgoing...)
	}
	msgs = msgs[:written]

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

	// Post-build bookkeeping (still under lock)
	now := time.Now()
	sentArrival := c.sendArrival - 1          // BuildPacket incremented after writing
	c.flushSendTimes[sentArrival&0x3FF] = now // #21: store per-seq send timestamp for RTT
	// PS2 connection_flush line 1292: conn.flags &= 0xfff0 — clears bits 0-3 atomically.
	c.arrivalDirty = false
	c.sessionAckPending = false
	c.ackFlushDeadline = time.Time{} // #26: clear 100ms ACK timer after flush
	// PS2 offset 0xa4 (line 1296): store current time as time_last_flush.
	c.lastFlushTime = now
	// PS2 connection_flush: while conn.flags & 0x40 is NOT set (instance_ack
	// not received), the timeout (m_usTimeout, the max RTT) grows by 1000 ms
	// per flush. The next RTT sample replaces it.
	if !c.handshakeComplete {
		c.rto += 1000 * time.Millisecond
		if c.rto > maxRTO {
			c.rto = maxRTO
		}
	}
	// #26: each reliable message written gets this send time and this flush
	// number (message_set_time / message_set_flush), retransmissions included.
	for _, msg := range msgs {
		if msg.Type.IsReliable() {
			c.unackedSendTime[msg.Sequence] = now
			c.unackedFlush[msg.Sequence] = sentArrival
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
		w.WriteByte(0x01)          // Has-token flag
		w.WriteBytes(sessionToken) // 32-byte session token
	}
	authData := authBuf[:w.Position()]
	// Not the bytes: they are the account name and the password-derived token.
	log.Printf("Authenticate payload (%d bytes, token %v)", len(authData), len(sessionToken) == 32)
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
		w.WriteByte(0x01)          // Has-token flag
		w.WriteBytes(sessionToken) // 32-byte session token
	}
	authData := authBuf[:w.Position()]
	log.Printf("Authenticate2 payload (%d bytes, token %v)", len(authData), len(sessionToken) == 32)
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

// ClearStateAck is called when a channel's entity is removed (objectID 0).
// It deliberately does NOTHING: the PS2 has no such step. A channel's
// sequence and its delta bases outlive the entity on it -- the server hands
// the channel to the next entity with the next sequence number and a delta
// against the last state we acked (114 reassignments in four PS2 captures,
// not one restart). Forgetting the bases made that delta undecodable, and
// forgetting rx would let the ack go backwards.
func (c *Connection) ClearStateAck(channel byte) {}

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
	c.delayed = make(map[uint16]*protocol.Message)
	c.arrivals = nil
	c.serverAckedReliable = 0
	c.flushAckMax = 0
	c.lastSentReliable = 0
	c.serverEndpoint = 0       // Reset — learn from first game server response (Sandstorm reuses endpoint pool, so game server may respond from a different endpoint than 0xB991)
	c.serverEndpointPacked = 0 // Reset — learned from server's DidServerInitiate local_endpoint
	c.instanceRemote = 0       // Reset — learned from server's NewInstance or SessionAck
	c.gotSessionAck = false
	c.handshakeComplete = false // Reset for new handshake
	c.handshakeSentCount = 0    // Reset handshake packet counter
	c.goodArrivalCount = 0      // Reset for new session (PS2 short flag 0x20)
	c.arrivalHistory = 0        // Reset arrival bitmap
	c.arrivalDirty = false
	c.sessionAckPending = false
	c.serverShort = false
	c.ackFlushDeadline = time.Time{}
	c.timedOut = false
	c.terminatePending = false
	c.terminateReason = 0
	c.connectPending = false
	c.lastFlushTime = time.Time{} // Reset for new session (PS2 0x200 flag cleared)
	c.rttSamples = [16]time.Duration{}
	c.rttSampleIdx = 0
	c.rto = initialRTO
	c.lastGuaranteedSendTime = time.Time{}
	c.flushSendTimes = [1024]time.Time{}
	c.lastSentReliable = 0
	c.didServerInit = false // Reset for game server session detection
	c.pktCount = 0          // Reset debug counter for game server phase
	c.unacked = nil
	c.unackedSendTime = make(map[uint16]time.Time)
	c.unackedFlush = make(map[uint16]uint16)
	c.outgoing = nil
	// A new connection: every state channel starts again at rx 0 with no bases.
	c.stateRx = make(map[byte]uint16)
	c.stateRxFlag = make(map[byte]byte)
	c.stateBases = make(map[byte]map[uint16][]byte)
}

// Metrics returns a snapshot of protocol health counters.
func (c *Connection) Metrics() ConnectionMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metrics
}

// updateRTT is connection_add_rtt_sample: a 16-deep ring of round trips
// (uns16 ms, so at most 65,535), and the timeout is the LARGEST of them.
// A message is due again at its send time + that timeout + 100 ms
// (connection_message_get_desired_flush_time), so RTO = max + 100 ms --
// not 2 x max, and with no floor or ceiling.
func (c *Connection) updateRTT(rtt time.Duration) {
	if rtt > 0xFFFF*time.Millisecond {
		rtt = 0xFFFF * time.Millisecond
	}
	c.rttSamples[c.rttSampleIdx&0x0F] = rtt
	c.rttSampleIdx++

	maxRTT := c.rttSamples[0]
	for _, s := range c.rttSamples[1:] {
		maxRTT = max(maxRTT, s)
	}
	c.rto = maxRTT + 100*time.Millisecond
}
