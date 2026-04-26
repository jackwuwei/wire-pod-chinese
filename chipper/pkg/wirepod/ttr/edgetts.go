package wirepod_ttr

import (
	"bytes"
	"context"
	"fmt"
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

// synthEdgeTTS calls the edge-tts CLI and returns 16kHz mono PCM chunks ready
// for streaming to the robot. Returns nil chunks (no error) for empty input.
func synthEdgeTTS(input string) ([][]byte, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	voice := vars.APIConfig.Knowledge.EdgeTTSVoice
	if voice == "" {
		voice = defaultEdgeTTSVoice
	}

	tmp, err := os.CreateTemp("", "wirepod-edgetts-*.mp3")
	if err != nil {
		return nil, err
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
		return nil, err
	}

	mp3Data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}

	decoder, err := mp3.NewDecoder(bytes.NewReader(mp3Data))
	if err != nil {
		logger.Println("edge-tts mp3 decode failed: " + err.Error())
		return nil, err
	}
	stereoPCM, err := io.ReadAll(decoder)
	if err != nil {
		logger.Println("edge-tts mp3 read failed: " + err.Error())
		return nil, err
	}

	monoPCM := stereoToMono16LE(stereoPCM)

	// Edge TTS default format is 24kHz mono MP3; go-mp3 outputs stereo PCM at
	// the source sample rate. Reuse the existing 24k→16k downsample/chunker.
	if rate := decoder.SampleRate(); rate != 24000 {
		logger.Println("edge-tts: unexpected sample rate from MP3 decoder, expected 24000 got ", rate)
	}
	return downsample24kTo16k(monoPCM), nil
}

// playEdgeTTSAudio streams pre-synthesized 16kHz PCM chunks to the robot and
// blocks until the robot reports playback complete via the bidi response
// stream.
//
// Two non-obvious requirements that broke earlier iterations:
//  1. Do NOT call CloseSend on the request stream. Vector's audio handler
//     closes the response stream as soon as it sees client EOF on the request
//     side, so PlaybackComplete is never delivered.
//  2. Drain the response stream until PlaybackComplete (or PlaybackFailure)
//     before returning. Without it the next call's AudioStreamPrepare races
//     the previous session's residual state and gets silently dropped — every
//     other pipelined sentence ends up inaudible.
func playEdgeTTSAudio(robot *vector.Vector, audioChunks [][]byte) error {
	if len(audioChunks) == 0 {
		return nil
	}

	var allChunks []byte
	for _, c := range audioChunks {
		allChunks = append(allChunks, c...)
	}
	// Bound total time by 2× expected audio length + 5s slack so a misbehaving
	// robot can't hang the consumer indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), pcmLength(allChunks)*2+5*time.Second)
	defer cancel()

	vclient, err := robot.Conn.ExternalAudioStreamPlayback(ctx)
	if err != nil {
		logger.Println("edge-tts: stream open failed: " + err.Error())
		return err
	}
	if err := vclient.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
			AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{
				AudioFrameRate: 16000,
				AudioVolume:    100,
			},
		},
	}); err != nil {
		logger.Println("edge-tts: prepare send failed: " + err.Error())
	}

	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		for _, chunk := range audioChunks {
			if err := vclient.Send(&vectorpb.ExternalAudioStreamRequest{
				AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
					AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{
						AudioChunkSizeBytes: 1024,
						AudioChunkSamples:   chunk,
					},
				},
			}); err != nil {
				logger.Println("edge-tts: chunk send failed: " + err.Error())
				return
			}
			time.Sleep(time.Millisecond * 25)
		}
		if err := vclient.Send(&vectorpb.ExternalAudioStreamRequest{
			AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
				AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{},
			},
		}); err != nil {
			logger.Println("edge-tts: complete send failed: " + err.Error())
		}
	}()

recvLoop:
	for {
		resp, err := vclient.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Println("edge-tts: recv failed: " + err.Error())
			break
		}
		switch resp.GetAudioResponseType().(type) {
		case *vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackComplete:
			break recvLoop
		case *vectorpb.ExternalAudioStreamResponse_AudioStreamBufferOverrun:
			ovr := resp.GetAudioStreamBufferOverrun()
			logger.Println(fmt.Sprintf("edge-tts: buffer overrun sent=%d played=%d", ovr.AudioSamplesSent, ovr.AudioSamplesPlayed))
		case *vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackFailyer:
			logger.Println("edge-tts: robot reported playback failure")
			break recvLoop
		}
	}
	<-senderDone
	return nil
}

func DoSayText_EdgeTTS(robot *vector.Vector, input string) error {
	chunks, err := synthEdgeTTS(input)
	if err != nil {
		return err
	}
	return playEdgeTTSAudio(robot, chunks)
}
