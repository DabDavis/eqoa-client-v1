package network

import (
	"fmt"
	"log"
	"sync"

	"github.com/eqoa/client/internal/protocol"
	"github.com/DabDavis/eqoa-esf-tools/pkg/buffer"
)

// packetBufPool reuses 1400-byte buffers for BuildPacket to avoid per-call
// allocation (~33 allocs/sec at 33 Hz flush rate). The returned result from
// AppendCRC is a separate allocation owned by the caller.
var packetBufPool = sync.Pool{
	New: func() any { return make([]byte, 1400) },
}

// ReceivedPacket holds a parsed incoming packet from the server.
type ReceivedPacket struct {
	LocalEndpoint  uint16 // Server's local endpoint
	RemoteEndpoint uint16 // Server's remote endpoint (our local endpoint)
	Header         *protocol.SegmentHeader
	Body           *protocol.SegmentBody
	SessionID      uint32 // From header if DidServerInitiate (truncated to 32-bit for ObjectID)
	SessionIDFull  uint64 // Full 64-bit local_endpoint varint from server (for REMOTE_EP_INLINE)
	RawData        []byte
}

// BuildPacket constructs an outgoing UDP packet from the client's perspective.
func BuildPacket(conn *Connection, messages []*protocol.Message) []byte {
	buf := packetBufPool.Get().([]byte)
	writer := buffer.NewWriter(buf)

	// Write endpoints: our local endpoint, then the remote (server) endpoint
	writer.WriteUint16(conn.localEndpoint)
	writer.WriteUint16(conn.remoteEndpoint)

	// Build segment header flags
	// Only set ClientInitiated. Do NOT set DidServerInitiate — the server's parser
	// would try to read a SessionID varint from our header, but we don't write one,
	// causing body parse corruption and guaranteed_ack being silently lost.
	var segFlags uint64
	segFlags |= uint64(protocol.SegmentFlagClientInitiated)

	// PS2 sends HasInstance + NewInstance only during handshake.
	// After handshake, PS2 sends just IsRemote|ClientInit (no HasInstance).
	// The server's connection.instance_remote is already set from the handshake,
	// so repeated HasInstance is unnecessary and adds parsing overhead.
	sendHasInstance := conn.hasInstance && !conn.handshakeComplete
	if sendHasInstance {
		segFlags |= uint64(protocol.SegmentFlagHasInstance)
		// Count handshake packets. The server needs at least 2 with HasInstance+SessionAck:
		//   Packet 1: server sets instance_remote, but instance_local_ok=false
		//   Packet 2: instance_remote matches → instance_local_ok=true → flags |= 0x40
		// Send 5 for safety (covers retransmits, out-of-order, etc.)
		conn.handshakeSentCount++
		if conn.handshakeSentCount >= 5 {
			conn.handshakeComplete = true
			log.Printf("Handshake complete: sent %d HasInstance+SessionAck packets", conn.handshakeSentCount)
		}
	}
	if conn.needsNewInst {
		segFlags |= uint64(protocol.SegmentFlagNewInstance)
		conn.needsNewInst = false
	}

	if conn.serverEndpointPacked != 0 {
		segFlags |= uint64(protocol.SegmentFlagIsRemote)
	}

	// Write segment header with placeholder body size (0x7FF for max 3-byte varint)
	segHeaderPos := writer.Position()
	flagsLen := segFlags | 0x7FF
	writer.Write7BitEncodedUInt64(flagsLen)

	// Write instance ID if hasInstance
	if sendHasInstance {
		writer.WriteUint32(conn.instanceID)
	}

	if conn.serverEndpointPacked != 0 {
		writer.Write7BitEncodedUInt64(conn.serverEndpointPacked)
	}

	// === Body starts here ===
	bodyStartPos := writer.Position()

	// Build body flags — PS2 connection_flush exact behavior.
	//
	// PS2 connection_flush:
	//   1. Check conn.flags bits 0-1: if either set → get_ack_mask → build guaranteed ack mask
	//   2. If conn.flags bit 2: set 0x10 (state ACK)
	//   3. If conn.flags bit 0: set 0x01 (FlushAck); if arrival_history != 0: set 0x05
	//   4. If conn.flags bit 3: set 0x40 (SessionAck)
	//   5. If counter < 0x11: set 0x20 (Short)
	//   6. Write: flags, instance(0x40), flush_seq, FlushAck+mask, GuaranteedAck+mask, StateACKs
	//
	// CRITICAL: If SegmentBody.read() returns false, ALL messages in the packet
	// are lost (including reliable messages). guaranteed_rx gets permanently stuck.
	var bodyFlags protocol.SegmentBodyFlags

	// FlushAck + GuaranteedAck: PS2 only sends when new arrivals received since last flush.
	// PS2 conn.flags bit 0 is set by record_segment, cleared at end of flush.
	// Our arrivalDirty flag mirrors this behavior (#18).
	if conn.arrivalDirty {
		bodyFlags |= protocol.BodyFlagFlushAck
		// PS2: if arrival_history != 0, also set FlushAck mask (0x04)
		if conn.arrivalHistory != 0 {
			bodyFlags |= protocol.BodyFlagUnknownPacked // 0x04 = FlushAck mask
		}
		// PS2: always sets GuaranteedAck (0x02) when arrivalDirty, even if
		// recvReliable==0 (sends seq=0). This is a no-op on the server since
		// its guaranteed_rx starts at 0, but matches PS2 behavior exactly.
		bodyFlags |= protocol.BodyFlagGuaranteedAck
	}

	// Build guaranteed ack mask from reliableOOO map (only when GuaranteedAck is set).
	// NOTE: conn.mu is already held by Flush — do NOT re-lock (Go mutexes aren't reentrant).
	var guaranteedAckMask int64
	if bodyFlags&protocol.BodyFlagGuaranteedAck != 0 {
		if len(conn.reliableOOO) > 0 {
			for seq := range conn.reliableOOO {
				offset := int(seq - conn.recvReliable - 1)
				if offset >= 0 && offset < 64 {
					guaranteedAckMask |= int64(1) << uint(offset)
				}
			}
		}
		if guaranteedAckMask != 0 {
			bodyFlags |= protocol.BodyFlagGuaranteedAckMask // 0x08
		}
	}

	// State channel ACKs: PS2 iterates all 248 channels, sends those with
	// dirty counter > 0, decrements counter per flush. We iterate only
	// channels with dirty count > 0 (functionally equivalent).
	var hasStateAcks bool
	var stateAcks []protocol.StateAck

	// PS2 connection_flush line 1167: state ACKs gated on conn.flags bit 2 (0x04).
	// Bit 2 is set by connection_record_acks/messages (un-decompiled), cleared each flush.
	// Bit 4 (0x10, set/cleared by remote's short flag) is NOT used to gate state ACKs.
	// Go approximation: handshakeComplete prevents premature ACKs before connection bootstrap;
	// per-channel stateAckDirtyCount provides the per-flush gating (equivalent to bit 2).
	// NOTE: conn.mu is already held by Flush — do NOT re-lock.
	const maxStateAcksPerPacket = 40 // Cap at 40 ACKs (120 bytes) to stay within MTU
	if conn.handshakeComplete {
		for ch := byte(0); ch < 0xF8; ch++ {
			if len(stateAcks) >= maxStateAcksPerPacket {
				break // remaining dirty channels will be sent next flush
			}
			if conn.stateAckDirtyCount[ch] > 0 {
				seq, ok := conn.stateAckMap[ch]
				if ok {
					if !hasStateAcks {
						stateAcks = make([]protocol.StateAck, 0, 29)
						hasStateAcks = true
					}
					stateAcks = append(stateAcks, protocol.StateAck{Channel: ch, Ack: seq})
					conn.stateAckDirtyCount[ch]-- // PS2: decrement per flush
				}
			}
		}

		// No need to sort — iterating 0..0xF7 produces ascending order already
		if hasStateAcks {
			bodyFlags |= protocol.BodyFlagClientUpdateAck
		}
	}

	// Body flag 0x20 ("short" flag): PS2 sets this when consecutive good arrivals < 17.
	// connection_flush line 1178: if (counter < 0x11) { flags |= 0x20; }
	// connection_record_segment: when 0x20 IS set, server clears bit 4 (0x10) in
	// connection.flags. When 0x20 is NOT set, server sets bit 4.
	if conn.goodArrivalCount < 17 {
		bodyFlags |= protocol.BodyFlagRdpMessage
	}

	// SessionAck (0x40 body flag): PS2 connection_record_segment sets flags |= 0x09
	// (bits 0+3) when NewInstance arrives. Bit 3 triggers SessionAck in next flush,
	// then is cleared. Unlike the old gotSessionAck boolean, this is per-arrival (#27).
	if conn.sessionAckPending {
		bodyFlags |= protocol.BodyFlagSessionAck
	}

	// Debug: log ACK values periodically (after all flags are set)
	if conn.sendArrival%50 == 0 || conn.sendArrival < 5 {
		log.Printf("BuildPacket: bodyFlags=0x%02X arrival=%d flushAck=%d guaranteedAck=%d serverAckedOurs=%d msgs=%d stateAcks=%d handshake=%v",
			byte(bodyFlags), conn.sendArrival, conn.recvArrival, conn.recvReliable,
			conn.serverAckedReliable, len(messages), len(stateAcks), conn.handshakeComplete)
	}

	// Log every state ACK packet with channel details (diagnostic for body rejection)
	if hasStateAcks {
		ackDetails := ""
		for _, ack := range stateAcks {
			ackDetails += fmt.Sprintf(" ch%d=%d", ack.Channel, ack.Ack)
		}
		log.Printf("STATE_ACK arrival=%d flags=0x%02X flushAck=%d guaranteedAck=%d:%s",
			conn.sendArrival, byte(bodyFlags), conn.recvArrival, conn.recvReliable, ackDetails)
	}

	writer.WriteByte(byte(bodyFlags))

	// Write SessionAck instance (must come before arrival, per DRDP body format).
	// Java Sandstorm SegmentBody.read(): compares body.instance_local against
	// connection.instance_remote (the CLIENT's instance from our header HasInstance).
	// So we must send OUR instance here, not the server's.
	// Handshake flow:
	//   Packet 1: header HasInstance = our instanceID → server stores as instance_remote
	//             body SessionAck = our instanceID → compared against instance_remote (still 0) → FAIL (expected)
	//   Packet 2: body SessionAck = our instanceID → matches instance_remote → instance_local_ok = true
	//             → recordAcks() called → connection.flags |= 0x40 → handshake complete
	if bodyFlags&protocol.BodyFlagSessionAck != 0 {
		writer.WriteUint32(conn.instanceID)
	}

	// Write arrival number (packet sequence) — PS2 writes THEN increments.
	// PS2/Java connection_flush: uVar1 = flush_seq; write(uVar1); flush_seq++;
	// flush_seq starts at 1 (not 0) — arrival 0 is treated as duplicate by Sandstorm.
	// First packet sends 1, second sends 2, etc.
	writer.WriteUint16(conn.sendArrival)
	conn.sendArrival++ // Increment AFTER write (PS2 behavior)

	// Write flush ack (last received arrival number)
	if bodyFlags&protocol.BodyFlagFlushAck != 0 {
		writer.WriteUint16(conn.recvArrival)
		// PS2: if flag 0x04, write arrival history bitmap
		if bodyFlags&protocol.BodyFlagUnknownPacked != 0 {
			writer.Write7BitEncodedInt64(int64(conn.arrivalHistory))
		}
	}

	// Write guaranteed ack (last received reliable message sequence)
	if bodyFlags&protocol.BodyFlagGuaranteedAck != 0 {
		writer.WriteUint16(conn.recvReliable)
		// PS2: if flag 0x08, write guaranteed ack mask
		if bodyFlags&protocol.BodyFlagGuaranteedAckMask != 0 {
			writer.Write7BitEncodedInt64(guaranteedAckMask)
		}
	}

	// Write state channel ACKs — already sorted ascending from iteration order
	if hasStateAcks {
		for _, ack := range stateAcks {
			writer.WriteByte(ack.Channel)
			writer.WriteUint16(ack.Ack)
		}
		writer.WriteByte(0xF8) // Terminator
	}

	// Write messages
	for _, msg := range messages {
		writeMessage(writer, msg)
	}

	// === Body ends here - patch segment header with actual body size ===
	actualBodySize := writer.Position() - bodyStartPos

	// Patch the segment header varint
	val := segFlags | uint64(actualBodySize)
	i := segHeaderPos
	for val >= 0x80 {
		buf[i] = byte(val&0x7F) | 0x80
		val >>= 7
		i++
	}
	buf[i] = byte(val)

	// Append CRC — AppendCRC returns a new slice, so we can return buf to the pool.
	data := writer.Bytes()
	result := protocol.AppendCRC(data)
	packetBufPool.Put(buf)

	// Debug: hex dump initial login packets (sendArrival <= 5) and first 10 post-promotion packets
	if conn.sendArrival <= 5 || (conn.didServerInit && (conn.sendArrival <= 10 || (hasStateAcks && conn.sendArrival <= 20))) {
		log.Printf("OUTGOING HEX arrival=%d (%d bytes): %X", conn.sendArrival-1, len(result), result)
	}

	return result
}

// writeMessage writes a single message into the packet buffer.
func writeMessage(writer *buffer.Writer, msg *protocol.Message) {
	// Write message type
	writer.WriteByte(byte(msg.Type))

	// Handle state channel messages (0x00-0xF7)
	if msg.Type.IsStateChannel() && !msg.Type.IsReliable() {
		rleData := protocol.RunLengthEncode(msg.Data())
		writer.WriteSize(msg.Size()) // Original (decoded) data size
		writer.WriteUint16(msg.Sequence)
		writer.WriteByte(msg.XORByte)
		writer.WriteBytes(rleData)
		return
	}

	// Write size (data includes opcode for reliable messages)
	writer.WriteSize(msg.Size())

	// Write sequence number for reliable messages
	if msg.Type.IsReliable() {
		writer.WriteUint16(msg.Sequence)
	}

	// Write message data
	writer.WriteBytes(msg.Data())
}

// ParsePacket parses an incoming UDP packet from the server.
func ParsePacket(data []byte, conn *Connection) (*ReceivedPacket, error) {
	if len(data) < 9 {
		return nil, fmt.Errorf("packet too short: %d bytes", len(data))
	}

	// Validate CRC
	if !protocol.ValidateCRC(data) {
		return nil, fmt.Errorf("invalid CRC")
	}

	// Strip CRC for parsing
	reader := buffer.NewReader(data[:len(data)-4])

	// Read endpoints (from server's perspective: server's local, server's remote)
	serverLocal, _ := reader.ReadUint16()
	serverRemote, _ := reader.ReadUint16()

	// Parse segment header
	header := &protocol.SegmentHeader{}
	if err := header.Read(reader); err != nil {
		return nil, fmt.Errorf("failed to read segment header (pkt %d bytes, endpoints %04X→%04X): %w",
			len(data), serverLocal, serverRemote, err)
	}

	pkt := &ReceivedPacket{
		LocalEndpoint:  serverLocal,
		RemoteEndpoint: serverRemote,
		Header:         header,
		RawData:        data,
	}

	// Check for reset connection with no body
	isReset := header.Flags&protocol.SegmentFlagResetConnection != 0 &&
		header.Flags&protocol.SegmentFlagHasInstance == 0
	if isReset {
		return pkt, nil
	}

	// If DidServerInitiate is set, the server's local_endpoint varint was already
	// consumed by SegmentHeader.Read() (step 5: NOT CLIENT_INITIATED → read varint).
	// Use it as the SessionID (PS2 echoes this back as remote_endpoint with 0x0800).
	if header.Flags&protocol.SegmentFlagDidServerInitiate != 0 {
		pkt.SessionID = uint32(header.LocalEndpoint)
		pkt.SessionIDFull = header.LocalEndpoint
	}

	// Empty body
	if header.Size == 0 {
		pkt.Body = protocol.NewSegmentBody()
		return pkt, nil
	}

	// Parse segment body
	body := protocol.NewSegmentBody()
	if err := body.Read(reader, header); err != nil {
		hexDump := fmt.Sprintf("%X", data)
		if len(hexDump) > 200 {
			hexDump = hexDump[:200] + "..."
		}
		return nil, fmt.Errorf("failed to read segment body (flags=0x%05X size=%d pos=%d rem=%d pkt=%d bytes): %w\n  hex: %s",
			header.Flags, header.Size, reader.Position(), reader.Remaining(), len(data), err, hexDump)
	}

	pkt.Body = body
	return pkt, nil
}
