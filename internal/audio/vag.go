package audio

// VAG ADPCM decoder — converts PS2 VAG audio blocks to 16-bit PCM.
// VAG format: 16-byte blocks, each containing 1 byte shift/predictor,
// 1 byte flags, 14 bytes of 4-bit ADPCM samples (28 samples per block).
//
// EQOA byte 0 layout (non-standard, verified from MIPS disassembly of AdpcmToPcm
// at 0x0043E190 and confirmed by data analysis — all high nibbles ≤4, low ≤12):
//   high nibble = predictor (0-4), low nibble = shift (0-12)
// This is opposite of standard PS2 SPU2 VAG (high=shift, low=predictor).

// VAG prediction coefficients (same as PS2 SPU2).
var vagCoeffs = [5][2]float64{
	{0.0, 0.0},
	{60.0 / 64.0, 0.0},
	{115.0 / 64.0, -52.0 / 64.0},
	{98.0 / 64.0, -55.0 / 64.0},
	{122.0 / 64.0, -60.0 / 64.0},
}

// DecodeVAG decodes PS2 VAG ADPCM data to signed 16-bit PCM samples.
// Each 16-byte VAG block produces 28 PCM samples.
func DecodeVAG(vagData []byte) []int16 {
	numBlocks := len(vagData) / 16
	samples := make([]int16, 0, numBlocks*28)

	var hist1, hist2 float64

	for block := 0; block < numBlocks; block++ {
		offset := block * 16
		if offset+16 > len(vagData) {
			break
		}

		// EQOA: high nibble = predictor, low nibble = shift
		// (confirmed by MIPS srav using low nibble, coeff table indexed by high nibble)
		predictor := int(vagData[offset] >> 4)
		shift := uint(vagData[offset] & 0x0F)
		flags := vagData[offset+1]

		// flags: 1 = loop end, 3 = loop start, 7 = end of data
		if flags == 7 {
			break
		}

		if predictor > 4 {
			predictor = 0
		}
		if shift > 12 {
			shift = 9 // PS2 clamps unusual shift values
		}
		coeff1 := vagCoeffs[predictor][0]
		coeff2 := vagCoeffs[predictor][1]

		// 14 data bytes = 28 nibbles = 28 samples
		for i := 0; i < 14; i++ {
			dataByte := vagData[offset+2+i]

			// Process two nibbles per byte (low nibble first, then high)
			for nibble := 0; nibble < 2; nibble++ {
				var n int32
				if nibble == 0 {
					n = int32(dataByte & 0x0F)
				} else {
					n = int32(dataByte >> 4)
				}

				// Sign-extend 4-bit to 32-bit
				if n > 7 {
					n -= 16
				}

				// Apply shift and prediction filter
				sample := float64(n<<12) / float64(uint32(1) << shift)
				sample += coeff1*hist1 + coeff2*hist2

				hist2 = hist1
				hist1 = sample

				// Clamp to int16 range
				out := int32(sample)
				if out > 32767 {
					out = 32767
				} else if out < -32768 {
					out = -32768
				}
				samples = append(samples, int16(out))
			}
		}
	}

	return samples
}

// detectInterleave auto-detects the stereo interleave size for a VAG file.
// BGM files from the main BGM directory use 0x10000 (65536) interleave,
// while shorter files from MUSIC directory (CC, TC, SS, etc.) use 0x4000 (16384).
// Detection: the R channel always starts with a silent (all-zero) VAG block.
func detectInterleave(vagData []byte) int {
	for _, il := range []int{0x10000, 0x4000, 0x2000} {
		if len(vagData) < il*2 {
			continue
		}
		if len(vagData)%(il*2) != 0 {
			continue
		}
		// Check if the R channel start (offset=il) is a silent block
		silent := true
		for i := 0; i < 16 && i+il < len(vagData); i++ {
			if vagData[il+i] != 0 {
				silent = false
				break
			}
		}
		if silent {
			return il
		}
	}
	// Fallback: try largest that divides evenly
	for _, il := range []int{0x10000, 0x4000, 0x2000} {
		if len(vagData) >= il*2 && len(vagData)%(il*2) == 0 {
			return il
		}
	}
	return 0x10000
}

// DecodeVAGStereo decodes chunk-interleaved stereo VAG data.
// Auto-detects interleave size: 0x10000 for zone BGMs, 0x4000 for menu/UI BGMs.
// Returns interleaved stereo PCM: [L0, R0, L1, R1, ...].
func DecodeVAGStereo(vagData []byte) []int16 {
	interleave := detectInterleave(vagData)

	if len(vagData) < interleave*2 {
		return nil
	}

	// Separate L/R channel data from interleaved chunks
	lBlocks := make([]byte, 0, len(vagData)/2)
	rBlocks := make([]byte, 0, len(vagData)/2)
	for off := 0; off+interleave*2 <= len(vagData); off += interleave * 2 {
		lBlocks = append(lBlocks, vagData[off:off+interleave]...)
		rBlocks = append(rBlocks, vagData[off+interleave:off+interleave*2]...)
	}

	left := DecodeVAG(lBlocks)
	right := DecodeVAG(rBlocks)

	// Interleave L,R
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	stereo := make([]int16, n*2)
	for i := 0; i < n; i++ {
		stereo[i*2] = left[i]
		stereo[i*2+1] = right[i]
	}
	return stereo
}
