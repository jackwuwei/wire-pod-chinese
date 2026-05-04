package wirepod_ttr

import (
	"encoding/binary"
	"math"
)

// stereoToMono16LE takes interleaved 16-bit little-endian stereo PCM and
// returns mono PCM by averaging the two channels per sample.
func stereoToMono16LE(stereo []byte) []byte {
	if len(stereo) < 4 {
		return nil
	}
	frames := len(stereo) / 4
	mono := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		l := int32(int16(binary.LittleEndian.Uint16(stereo[i*4 : i*4+2])))
		r := int32(int16(binary.LittleEndian.Uint16(stereo[i*4+2 : i*4+4])))
		avg := int16((l + r) / 2)
		binary.LittleEndian.PutUint16(mono[i*2:], uint16(avg))
	}
	return mono
}

func bytesToInt16s(data []byte) []int16 {
	int16s := make([]int16, len(data)/2)
	for i := range int16s {
		int16s[i] = int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
	}
	return int16s
}

func int16sToBytes(data []int16) []byte {
	bytes := make([]byte, len(data)*2)
	for i, val := range data {
		binary.LittleEndian.PutUint16(bytes[i*2:], uint16(val))
	}
	return bytes
}

func downsample24kTo16k(input []byte) [][]byte {
	outBytes := downsample24kTo16kLinear(input)
	filteredBytes := lowPassFilter(outBytes, 4000, 16000)
	iVolBytes := increaseVolume(filteredBytes, 5)
	return chunkPCM16(iVolBytes, 1024)
}

// downsample32kTo16k decimates 2:1 with a low-pass filter to suppress
// aliasing. Output is 16kHz mono PCM-16 chunked into 1024-byte frames ready
// for ExternalAudioStreamPlayback. No volume boost — sovits/serve.py already
// peak-normalizes and applies +9dB gain server-side.
func downsample32kTo16k(input []byte) [][]byte {
	filtered := lowPassFilter(input, 7000, 32000)
	in16 := bytesToInt16s(filtered)
	out16 := make([]int16, len(in16)/2)
	for i := range out16 {
		out16[i] = in16[i*2]
	}
	return chunkPCM16(int16sToBytes(out16), 1024)
}

// chunkPCM16 splits a PCM stream into chunks of `size` bytes, zero-padding
// the final chunk if needed (Vector's audio handler expects fixed-size frames).
func chunkPCM16(data []byte, size int) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		if len(data) < size {
			chunk := make([]byte, size)
			copy(chunk, data)
			out = append(out, chunk)
			break
		}
		out = append(out, data[:size])
		data = data[size:]
	}
	return out
}

func increaseVolume(data []byte, factor float64) []byte {
	int16s := bytesToInt16s(data)

	for i := range int16s {
		scaled := float64(int16s[i]) * factor
		if scaled > math.MaxInt16 {
			int16s[i] = math.MaxInt16
		} else if scaled < math.MinInt16 {
			int16s[i] = math.MinInt16
		} else {
			int16s[i] = int16(scaled)
		}
	}

	return int16sToBytes(int16s)
}

// this is copied
func lowPassFilter(data []byte, cutoffFreq float64, sampleRate int) []byte {
	int16s := bytesToInt16s(data)
	filtered := make([]int16, len(int16s))
	rc := 1.0 / (2 * 3.1416 * cutoffFreq)
	dt := 1.0 / float64(sampleRate)
	alpha := dt / (rc + dt)
	filtered[0] = int16s[0]
	for i := 1; i < len(int16s); i++ {
		current := alpha*float64(int16s[i]) + (1-alpha)*float64(filtered[i-1])
		filtered[i] = int16(current)
	}

	return int16sToBytes(filtered)
}

// copied too
func downsample24kTo16kLinear(input []byte) []byte {
	int16s := bytesToInt16s(input)
	outputLength := (len(int16s) * 2) / 3
	output := make([]int16, outputLength)

	j := 0
	for i := 0; i < len(int16s)-2; i += 3 {
		first := (2*int32(int16s[i]) + int32(int16s[i+1])) / 3
		second := (int32(int16s[i+1]) + 2*int32(int16s[i+2])) / 3
		output[j] = int16(first)
		output[j+1] = int16(second)
		j += 2
	}

	return int16sToBytes(output)
}
