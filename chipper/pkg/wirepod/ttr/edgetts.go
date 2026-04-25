package wirepod_ttr

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/hajimehoshi/go-mp3"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

const (
	defaultEdgeTTSVoice  = "zh-CN-XiaoxiaoNeural"
	defaultEdgeTTSPython = "./.venv/bin/python3"
	edgeTTSTimeout       = 30 * time.Second
)

func edgeTTSPython() string {
	if p := os.Getenv("WIREPOD_EDGETTS_PYTHON"); p != "" {
		return p
	}
	return defaultEdgeTTSPython
}

func DoSayText_EdgeTTS(robot *vector.Vector, input string) error {
	if strings.TrimSpace(input) == "" {
		return nil
	}

	voice := vars.APIConfig.Knowledge.EdgeTTSVoice
	if voice == "" {
		voice = defaultEdgeTTSVoice
	}

	tmp, err := os.CreateTemp("", "wirepod-edgetts-*.mp3")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	ctx, cancel := context.WithTimeout(context.Background(), edgeTTSTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, edgeTTSPython(), "-m", "edge_tts",
		"--voice", voice, "--text", input, "--write-media", tmpPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		logger.Println("edge-tts CLI failed: " + err.Error() + " | " + strings.TrimSpace(stderr.String()))
		return err
	}

	mp3Data, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}

	decoder, err := mp3.NewDecoder(bytes.NewReader(mp3Data))
	if err != nil {
		logger.Println("edge-tts mp3 decode failed: " + err.Error())
		return err
	}
	stereoPCM, err := io.ReadAll(decoder)
	if err != nil {
		logger.Println("edge-tts mp3 read failed: " + err.Error())
		return err
	}

	monoPCM := stereoToMono16LE(stereoPCM)

	// Edge TTS default format is 24kHz mono MP3; go-mp3 outputs stereo PCM at
	// the source sample rate. Reuse the existing 24k→16k downsample/chunker.
	// If the source rate ever differs we fall back to streaming as-is, which
	// will sound off-pitch — log it so we notice.
	if rate := decoder.SampleRate(); rate != 24000 {
		logger.Println("edge-tts: unexpected sample rate from MP3 decoder, expected 24000 got ", rate)
	}
	audioChunks := downsample24kTo16k(monoPCM)

	vclient, err := robot.Conn.ExternalAudioStreamPlayback(context.Background())
	if err != nil {
		return err
	}
	vclient.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
			AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{
				AudioFrameRate: 16000,
				AudioVolume:    100,
			},
		},
	})

	var allChunks []byte
	for _, c := range audioChunks {
		allChunks = append(allChunks, c...)
	}

	go func() {
		for _, chunk := range audioChunks {
			vclient.Send(&vectorpb.ExternalAudioStreamRequest{
				AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
					AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{
						AudioChunkSizeBytes: 1024,
						AudioChunkSamples:   chunk,
					},
				},
			})
			time.Sleep(time.Millisecond * 25)
		}
		vclient.Send(&vectorpb.ExternalAudioStreamRequest{
			AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
				AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{},
			},
		})
	}()
	time.Sleep(pcmLength(allChunks) + (time.Millisecond * 50))
	return nil
}
