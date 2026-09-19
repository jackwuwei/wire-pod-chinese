package wirepod_ttr

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

const (
	sovitsDefaultURL  = "http://127.0.0.1:8020/tts"
	sovitsDefaultLang = "中英混合"
	// Long sentences on Pentium 8505 take ~14s wall for ~8s audio. Allow 60s
	// to cover slowest case + some headroom; serve.py serializes via _lock so
	// queued calls can stack.
	sovitsTimeout = 60 * time.Second
)

type sovitsRequest struct {
	Text           string   `json:"text"`
	Lang           string   `json:"lang,omitempty"`
	RefAudioPath   string   `json:"ref_audio_path,omitempty"`
	RefText        string   `json:"ref_text,omitempty"`
	RefLang        string   `json:"ref_lang,omitempty"`
	TargetPeakDBFS *float32 `json:"target_peak_dbfs,omitempty"`
	GainDB         *float32 `json:"gain_db,omitempty"`
}

func sovitsURL() string {
	if u := strings.TrimSpace(vars.APIConfig.Knowledge.SoVITSURL); u != "" {
		return u
	}
	return sovitsDefaultURL
}

// synthSoVITS hits the wire-pod-pinned GPT-SoVITS HTTP endpoint, parses the
// returned WAV (32kHz mono PCM-16), and returns 16kHz chunked PCM ready for
// streaming. Returns nil chunks (no error) on empty input.
func synthSoVITS(input string) ([][]byte, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	lang := strings.TrimSpace(vars.APIConfig.Knowledge.SoVITSLang)
	if lang == "" {
		lang = sovitsDefaultLang
	}

	body := sovitsRequest{
		Text:         input,
		Lang:         lang,
		RefAudioPath: strings.TrimSpace(vars.APIConfig.Knowledge.SoVITSRefAudio),
		RefText:      strings.TrimSpace(vars.APIConfig.Knowledge.SoVITSRefText),
		RefLang:      strings.TrimSpace(vars.APIConfig.Knowledge.SoVITSRefLang),
	}
	if g := vars.APIConfig.Knowledge.SoVITSGainDB; g != 0 {
		body.GainDB = &g
	}
	if p := vars.APIConfig.Knowledge.SoVITSPeakDBFS; p != 0 {
		body.TargetPeakDBFS = &p
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), sovitsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sovitsURL(), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Println("sovits HTTP failed: " + err.Error())
		return nil, err
	}
	defer resp.Body.Close()
	wavBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sovits HTTP %d: %s", resp.StatusCode, string(wavBytes))
	}

	pcm, sampleRate, channels, err := parsePCMWAV(wavBytes)
	if err != nil {
		logger.Println("sovits WAV parse failed: " + err.Error())
		return nil, err
	}
	if channels != 1 {
		logger.Println(fmt.Sprintf("sovits: expected mono, got %d channels — flattening", channels))
		if channels == 2 {
			pcm = stereoToMono16LE(pcm)
		} else {
			return nil, fmt.Errorf("sovits: unsupported channel count %d", channels)
		}
	}
	switch sampleRate {
	case 32000:
		return downsample32kTo16k(pcm), nil
	case 16000:
		return chunkPCM16(pcm, 1024), nil
	default:
		// We could implement arbitrary resampling, but every observed serve.py
		// response is 32kHz. Bail loudly so a misconfiguration doesn't ship
		// silently corrupted audio.
		return nil, fmt.Errorf("sovits: unsupported sample rate %d (expected 32000 or 16000)", sampleRate)
	}
}

// parsePCMWAV finds the data chunk in a canonical RIFF/WAVE PCM stream and
// returns the raw PCM bytes plus sample rate and channel count. Tolerates
// extra chunks (LIST, fact) between fmt and data.
func parsePCMWAV(data []byte) ([]byte, uint32, uint16, error) {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	var sampleRate uint32
	var channels, bits uint16
	i := 12
	for i+8 <= len(data) {
		chunkID := string(data[i : i+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		body := i + 8
		if body+chunkSize > len(data) {
			return nil, 0, 0, fmt.Errorf("truncated chunk %q", chunkID)
		}
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, 0, 0, fmt.Errorf("fmt chunk too small: %d", chunkSize)
			}
			format := binary.LittleEndian.Uint16(data[body : body+2])
			if format != 1 {
				return nil, 0, 0, fmt.Errorf("not PCM (format=%d)", format)
			}
			channels = binary.LittleEndian.Uint16(data[body+2 : body+4])
			sampleRate = binary.LittleEndian.Uint32(data[body+4 : body+8])
			bits = binary.LittleEndian.Uint16(data[body+14 : body+16])
			if bits != 16 {
				return nil, 0, 0, fmt.Errorf("expected 16-bit PCM, got %d", bits)
			}
		case "data":
			return data[body : body+chunkSize], sampleRate, channels, nil
		}
		// Pad to even length per RIFF spec.
		i = body + chunkSize
		if chunkSize%2 == 1 {
			i++
		}
	}
	return nil, 0, 0, fmt.Errorf("data chunk not found")
}

func DoSayText_SoVITS(robot *vector.Vector, input string) error {
	chunks, err := synthSoVITS(input)
	if err != nil {
		return err
	}
	// Reuse the edge-tts playback path — it's provider-agnostic, just streams
	// 16kHz mono PCM frames over the gRPC ExternalAudioStreamPlayback channel.
	return playEdgeTTSAudio(robot, chunks)
}
