package audio

import (
	"os"
	"sync"
)

const (
	bgmSampleRate = 44100
	bgmChannels   = 2
	// Decode buffer: 1470 stereo frames = ~33ms of audio.
	// Matches raylib's default AudioStream sub-buffer (sampleRate/30).
	bgmDecodeFrames = 1470
)

// BGMStream streams VAG audio from an ISO file without loading it all into memory.
// Reads and decodes small chunks on demand, keeping only ~32KB in RAM.
type BGMStream struct {
	mu sync.Mutex

	// ISO source
	file       *os.File
	fileOffset int64 // start of BGM data in ISO
	fileSize   int64 // total BGM data size

	// Interleave layout
	interleave int // block interleave size (0x10000 or 0x4000)

	// Read position tracking
	readPos int64 // current byte offset within BGM data

	// VAG decoder state (persistent across blocks — ADPCM needs history)
	leftHist1, leftHist2   float64
	rightHist1, rightHist2 float64

	// Reusable read buffer for ISO reads (avoids per-chunk allocation)
	readBuf []byte

	// Pre-decoded PCM buffer for feeding to raylib
	pcmBuf []int16 // decoded stereo PCM ready to play
	pcmPos int     // read position within pcmBuf

	// State
	playing bool
	key     string
}

// NewBGMStream creates a streaming BGM player from an ISO file region.
func NewBGMStream(isoPath string, offset, size int64) (*BGMStream, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, err
	}

	// Read first bytes to detect interleave
	header := make([]byte, 0x20002)
	f.ReadAt(header, offset)
	interleave := detectInterleaveFromHeader(header, size)

	buf := make([]int16, bgmDecodeFrames*bgmChannels)
	return &BGMStream{
		file:       f,
		fileOffset: offset,
		fileSize:   size,
		interleave: interleave,
		pcmBuf:     buf,
		pcmPos:     len(buf), // force decode on first FillBuffer call
	}, nil
}

// detectInterleaveFromHeader checks the first few bytes to determine interleave.
func detectInterleaveFromHeader(header []byte, totalSize int64) int {
	for _, il := range []int{0x10000, 0x4000, 0x2000} {
		if totalSize < int64(il*2) {
			continue
		}
		if totalSize%int64(il*2) != 0 {
			continue
		}
		// Check if R channel start is silent
		if il+16 <= len(header) {
			silent := true
			for i := 0; i < 16; i++ {
				if header[il+i] != 0 {
					silent = false
					break
				}
			}
			if silent {
				return il
			}
		}
	}
	return 0x10000
}

// Close releases the file handle.
func (b *BGMStream) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		b.file.Close()
		b.file = nil
	}
	b.playing = false
}

// Reset seeks back to the beginning for looping.
func (b *BGMStream) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.readPos = 0
	b.pcmPos = len(b.pcmBuf) // force refill
	b.leftHist1 = 0
	b.leftHist2 = 0
	b.rightHist1 = 0
	b.rightHist2 = 0
}

// FillBuffer decodes the next chunk of PCM data into the provided float32 buffer.
// Returns the number of frames filled (0 = end of track).
// The output buffer is stereo interleaved float32 [-1.0, 1.0].
func (b *BGMStream) FillBuffer(out []float32, maxFrames int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.file == nil {
		return 0
	}

	filled := 0
	for filled < maxFrames {
		// If PCM buffer exhausted, decode more from ISO
		if b.pcmPos >= len(b.pcmBuf) {
			n := b.decodeNextChunk()
			if n == 0 {
				break // end of track
			}
		}

		// Copy from PCM buffer to output (convert int16 → float32)
		remaining := (len(b.pcmBuf) - b.pcmPos) / bgmChannels
		if remaining > maxFrames-filled {
			remaining = maxFrames - filled
		}
		for i := 0; i < remaining; i++ {
			idx := (filled + i) * bgmChannels
			srcIdx := b.pcmPos + i*bgmChannels
			out[idx] = float32(b.pcmBuf[srcIdx]) / 32768.0
			out[idx+1] = float32(b.pcmBuf[srcIdx+1]) / 32768.0
		}
		b.pcmPos += remaining * bgmChannels
		filled += remaining
	}
	return filled
}

// decodeNextChunk reads one full interleave pair from ISO and decodes
// all VAG blocks to pcmBuf. Returns number of stereo frames decoded.
// pcmBuf may hold thousands of frames; FillBuffer serves them in small chunks.
func (b *BGMStream) decodeNextChunk() int {
	if b.readPos >= b.fileSize {
		return 0
	}

	// Read one full interleave pair: [L chunk][R chunk]
	readSize := b.interleave * 2
	if int64(readSize) > b.fileSize-b.readPos {
		readSize = int(b.fileSize - b.readPos)
	}
	if readSize < 32 {
		return 0
	}

	// Reuse read buffer to avoid per-chunk allocation
	if b.readBuf == nil || len(b.readBuf) < readSize {
		b.readBuf = make([]byte, readSize)
	}
	n, err := b.file.ReadAt(b.readBuf[:readSize], b.fileOffset+b.readPos)
	if err != nil || n < 32 {
		return 0
	}
	b.readPos += int64(n)

	// Split L/R from interleaved chunk
	lSize := b.interleave
	if lSize > n/2 {
		lSize = n / 2
	}
	rSize := n - lSize

	lData := b.readBuf[:lSize]
	rData := b.readBuf[lSize : lSize+rSize]

	// Decode ALL blocks in both channels with persistent history
	lSamples := b.decodeVAGBlock(lData, &b.leftHist1, &b.leftHist2, lSize)
	rSamples := b.decodeVAGBlock(rData, &b.rightHist1, &b.rightHist2, rSize)

	// Interleave L/R into pcmBuf
	frames := len(lSamples)
	if len(rSamples) < frames {
		frames = len(rSamples)
	}

	needed := frames * bgmChannels
	if cap(b.pcmBuf) < needed {
		b.pcmBuf = make([]int16, needed)
	}
	b.pcmBuf = b.pcmBuf[:needed]
	for i := 0; i < frames; i++ {
		b.pcmBuf[i*2] = lSamples[i]
		b.pcmBuf[i*2+1] = rSamples[i]
	}
	b.pcmPos = 0
	return frames
}

// decodeVAGBlock decodes up to maxBytes of VAG data with persistent ADPCM history.
// Uses the same math as DecodeVAG in vag.go (verified working).
func (b *BGMStream) decodeVAGBlock(data []byte, hist1, hist2 *float64, maxBytes int) []int16 {
	numBlocks := len(data) / 16
	if numBlocks > maxBytes/16 {
		numBlocks = maxBytes / 16
	}
	samples := make([]int16, 0, numBlocks*28)

	for block := 0; block < numBlocks; block++ {
		offset := block * 16
		if offset+16 > len(data) {
			break
		}

		predictor := int(data[offset] >> 4)
		shift := uint(data[offset] & 0x0F)
		flags := data[offset+1]

		if flags == 7 {
			break
		}
		if predictor > 4 {
			predictor = 0
		}
		if shift > 12 {
			shift = 9
		}

		coef1 := vagCoeffs[predictor][0]
		coef2 := vagCoeffs[predictor][1]

		for i := 0; i < 14; i++ {
			dataByte := data[offset+2+i]
			for nibble := 0; nibble < 2; nibble++ {
				var n int32
				if nibble == 0 {
					n = int32(dataByte & 0x0F)
				} else {
					n = int32(dataByte >> 4)
				}
				if n > 7 {
					n -= 16
				}

				sample := float64(n<<12) / float64(uint32(1)<<shift)
				sample += coef1**hist1 + coef2**hist2
				*hist2 = *hist1
				*hist1 = sample

				clamped := int16(sample)
				if sample > 32767 {
					clamped = 32767
				} else if sample < -32768 {
					clamped = -32768
				}
				samples = append(samples, clamped)
			}
		}
	}
	return samples
}

// FillInt16 fills the output buffer with decoded int16 stereo PCM.
// Returns the number of frames filled.
func (b *BGMStream) FillInt16(out []int16, maxFrames int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.file == nil {
		return 0
	}

	filled := 0
	for filled < maxFrames {
		if b.pcmPos >= len(b.pcmBuf) {
			n := b.decodeNextChunk()
			if n == 0 {
				break
			}
		}

		remaining := (len(b.pcmBuf) - b.pcmPos) / bgmChannels
		if remaining > maxFrames-filled {
			remaining = maxFrames - filled
		}
		copy(out[filled*bgmChannels:], b.pcmBuf[b.pcmPos:b.pcmPos+remaining*bgmChannels])
		b.pcmPos += remaining * bgmChannels
		filled += remaining
	}
	return filled
}

/*
// PlayStreamBGM starts streaming a BGM track from the ISO.
// Currently unused — using raylib Music type instead (see audio.go PlayBGMData).
func (s *System) PlayStreamBGM(isoPath, bgmFileName string, volume float32) {
	if s == nil {
		return
	}

	loc, ok := esf.ISOGetFileLocation(bgmFileName)
	if !ok {
		log.Printf("Audio: unknown BGM file %q", bgmFileName)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bgmActive && s.bgmPath == bgmFileName {
		return // already playing this track
	}
	s.stopBGMLocked()

	stream, err := NewBGMStream(isoPath, loc.Offset, loc.Size)
	if err != nil {
		log.Printf("Audio: failed to open BGM stream: %v", err)
		return
	}

	s.bgmStream = stream
	s.bgmActive = true
	s.bgmPath = bgmFileName
	s.bgmVol = volume

	log.Printf("Audio: streaming BGM %s (%.1f MB, interleave=%d)",
		bgmFileName, float64(loc.Size)/(1024*1024), stream.interleave)
}

// UpdateBGMStream should be called from the main loop to feed audio data.
// Returns true if still playing.
func (s *System) UpdateBGMStream() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bgmStream == nil || !s.bgmActive {
		return false
	}

	// Create raylib audio stream on first call and fill initial buffer.
	if s.bgmRLStream.Buffer == nil {
		s.bgmRLStream = rl.LoadAudioStream(bgmSampleRate, 16, bgmChannels) // 16-bit int
		rl.SetAudioStreamVolume(s.bgmRLStream, s.bgmVol)
		s.bgmInt16Buf = make([]int16, bgmDecodeFrames*bgmChannels)
		// Pre-fill first buffer so playback starts immediately.
		n := s.bgmStream.FillInt16(s.bgmInt16Buf, bgmDecodeFrames)
		log.Printf("Audio: BGM stream created, pre-filled %d frames, valid=%v", n, rl.IsAudioStreamValid(s.bgmRLStream))
		if n > 0 {
			rl.UpdateAudioStream(s.bgmRLStream, s.bgmInt16Buf[:n*bgmChannels])
		}
		rl.PlayAudioStream(s.bgmRLStream)
	}

	if !s.bgmStreamLogged {
		log.Printf("Audio: BGM stream update — playing=%v", rl.IsAudioStreamPlaying(s.bgmRLStream))
		s.bgmStreamLogged = true
	}
	// Feed multiple buffers per frame to prevent underrun.
	// At 44100Hz/480 frames = ~92 buffers/sec needed; at 60fps = ~1.5 per frame.
	for i := 0; i < 4; i++ {
		if !rl.IsAudioStreamProcessed(s.bgmRLStream) {
			break
		}
		buf := s.bgmInt16Buf
		n := s.bgmStream.FillInt16(buf, bgmDecodeFrames)
		if n == 0 {
			// End of track — loop
			s.bgmStream.Reset()
			n = s.bgmStream.FillInt16(buf, bgmDecodeFrames)
			if n == 0 {
				return false // truly empty
			}
		}
		rl.UpdateAudioStream(s.bgmRLStream, buf[:n*bgmChannels])
	}
	return true
}
*/
