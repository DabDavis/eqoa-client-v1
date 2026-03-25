package audio

import (
	"os"
	"testing"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

const testISO = "/home/sdg/Documents/eqoa/EverQuest - Online Adventures - Frontiers (USA).iso"

func TestBGMStreamDecode(t *testing.T) {
	if _, err := os.Stat(testISO); err != nil {
		t.Skip("ISO not found")
	}

	loc, ok := esf.ISOGetFileLocation("FREEPORT.BGM")
	if !ok {
		t.Fatal("FREEPORT.BGM not in ISO table")
	}
	t.Logf("FREEPORT.BGM: offset=%d size=%d (%.1f MB)", loc.Offset, loc.Size, float64(loc.Size)/(1024*1024))

	stream, err := NewBGMStream(testISO, loc.Offset, loc.Size)
	if err != nil {
		t.Fatalf("NewBGMStream: %v", err)
	}
	defer stream.Close()

	t.Logf("interleave: %d (0x%X)", stream.interleave, stream.interleave)
	t.Logf("pcmPos: %d, pcmBuf len: %d", stream.pcmPos, len(stream.pcmBuf))

	// Fill a buffer
	buf := make([]float32, 480*2)
	frames := stream.FillBuffer(buf, 480)
	t.Logf("FillBuffer returned %d frames", frames)

	if frames == 0 {
		t.Fatal("FillBuffer returned 0 frames")
	}

	// Check for non-zero samples
	nonZero := 0
	maxVal := float32(0)
	for _, v := range buf[:frames*2] {
		if v != 0 {
			nonZero++
		}
		if v > maxVal {
			maxVal = v
		}
		if -v > maxVal {
			maxVal = -v
		}
	}
	t.Logf("Non-zero samples: %d/%d (%.1f%%)", nonZero, frames*2, float64(nonZero)/float64(frames*2)*100)
	t.Logf("Max sample value: %.4f", maxVal)
	t.Logf("First 8 samples: [%.4f, %.4f, %.4f, %.4f, %.4f, %.4f, %.4f, %.4f]",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5], buf[6], buf[7])

	// First chunk may be silent — decode more chunks to find audio
	totalNonZero := nonZero
	for chunk := 1; chunk < 20 && totalNonZero == 0; chunk++ {
		frames = stream.FillBuffer(buf, 480)
		if frames == 0 {
			break
		}
		for _, v := range buf[:frames*2] {
			if v != 0 {
				totalNonZero++
			}
		}
	}
	t.Logf("After scanning more chunks: %d non-zero samples", totalNonZero)
	if totalNonZero == 0 {
		t.Error("All samples are zero after 20 chunks — decoder is broken")
	}

	// Compare with working DecodeVAGStereo on same data
	rawData, err := esf.ReadISOFileRaw(testISO, "FREEPORT.BGM")
	if err != nil {
		t.Fatalf("ReadISOFileRaw: %v", err)
	}
	refPCM := DecodeVAGStereo(rawData)
	t.Logf("Reference decoder: %d stereo samples", len(refPCM)/2)

	// Find first non-zero in reference
	for i, v := range refPCM {
		if v != 0 {
			t.Logf("Reference first non-zero at sample %d (frame %d, %.2fs): %d",
				i, i/2, float64(i/2)/44100.0, v)
			break
		}
	}

	// Compare first 100 frames
	mismatches := 0
	for i := 0; i < 200 && i < len(refPCM); i++ {
		streamVal := int16(buf[i] * 32768)
		refVal := refPCM[i]
		diff := int(streamVal) - int(refVal)
		if diff > 1 || diff < -1 { // allow ±1 rounding
			if mismatches < 5 {
				t.Logf("Mismatch at sample %d: stream=%d ref=%d diff=%d", i, streamVal, refVal, diff)
			}
			mismatches++
		}
	}
	t.Logf("Mismatches in first 100 frames: %d/200", mismatches)
	if mismatches > 10 {
		t.Errorf("Too many mismatches: %d/200", mismatches)
	}
}
