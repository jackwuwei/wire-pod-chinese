package wirepod_sherpaonnx

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
)

var Name string = "sherpa-onnx"

var (
	recognizer    *sherpa.OfflineRecognizer
	recognizerMu  sync.Mutex
	recognizerOk  bool
	currentLang   string
	currentSubdir string
)

const (
	defaultModelDir = "sense-voice"
	modelFileName   = "model.int8.onnx"
	tokensFileName  = "tokens.txt"
	sampleRate      = 16000
	minDurationMs   = 1000
)

func resolveLanguage(sttLanguage string) string {
	if v := os.Getenv("SHERPA_LANGUAGE"); v != "" {
		return v
	}
	switch strings.ToLower(strings.Split(sttLanguage, "-")[0]) {
	case "zh":
		return "zh"
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "ko":
		return "ko"
	case "yue":
		return "yue"
	default:
		return "auto"
	}
}

func resolveModelDir() string {
	subdir := os.Getenv("SHERPA_MODEL_DIR")
	if subdir == "" {
		subdir = defaultModelDir
	}
	return subdir
}

func Init() error {
	if !vars.APIConfig.PastInitialSetup {
		return nil
	}

	recognizerMu.Lock()
	defer recognizerMu.Unlock()

	subdir := resolveModelDir()
	lang := resolveLanguage(vars.APIConfig.STT.Language)

	if recognizerOk && subdir == currentSubdir && lang == currentLang {
		return nil
	}

	if recognizerOk {
		logger.Println("Reloading sherpa-onnx recognizer")
		sherpa.DeleteOfflineRecognizer(recognizer)
		recognizer = nil
		recognizerOk = false
	}

	modelDir := filepath.Join(vars.SherpaOnnxModelPath, subdir)
	modelPath := filepath.Join(modelDir, modelFileName)
	tokensPath := filepath.Join(modelDir, tokensFileName)

	if _, err := os.Stat(modelPath); err != nil {
		logger.Println("Sherpa-onnx model not found: " + modelPath)
		return err
	}
	if _, err := os.Stat(tokensPath); err != nil {
		logger.Println("Sherpa-onnx tokens file not found: " + tokensPath)
		return err
	}

	logger.Println("Opening sherpa-onnx SenseVoice model (" + modelPath + ", language=" + lang + ")")

	threads := runtime.NumCPU()
	if threads > 4 {
		threads = 4
	}

	config := sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{
			SampleRate: sampleRate,
			FeatureDim: 80,
		},
		ModelConfig: sherpa.OfflineModelConfig{
			SenseVoice: sherpa.OfflineSenseVoiceModelConfig{
				Model:                       modelPath,
				Language:                    lang,
				UseInverseTextNormalization: 1,
			},
			Tokens:     tokensPath,
			NumThreads: threads,
			Provider:   "cpu",
			Debug:      0,
			ModelType:  "sense-voice",
		},
		DecodingMethod: "greedy_search",
	}

	rec := sherpa.NewOfflineRecognizer(&config)
	if rec == nil {
		return errors.New("sherpa-onnx: NewOfflineRecognizer returned nil")
	}
	recognizer = rec
	recognizerOk = true
	currentLang = lang
	currentSubdir = subdir

	logger.Println("Sherpa-onnx initiated successfully")
	return nil
}

func STT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", Sherpa-Onnx) Processing...")

	for {
		_, err := req.GetNextStreamChunk()
		if err != nil {
			return "", err
		}
		speechIsDone, _ := req.DetectEndOfSpeech()
		if speechIsDone {
			break
		}
	}

	pcm := padPCM(req.DecodedMicData)
	samples := bytesToFloat32(pcm)

	recognizerMu.Lock()
	if !recognizerOk {
		recognizerMu.Unlock()
		return "", errors.New("sherpa-onnx: recognizer not initialized")
	}
	stream := sherpa.NewOfflineStream(recognizer)
	stream.AcceptWaveform(sampleRate, samples)
	recognizer.Decode(stream)
	result := stream.GetResult()
	sherpa.DeleteOfflineStream(stream)
	recognizerMu.Unlock()

	transcribedText := strings.TrimSpace(result.Text)
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}

func padPCM(data []byte) []byte {
	const bytesPerSample = 2
	const minSamples = sampleRate * minDurationMs / 1000

	if len(data)/bytesPerSample >= minSamples {
		return data
	}
	pad := make([]byte, minSamples*bytesPerSample-len(data))
	return append(data, pad...)
}

func bytesToFloat32(buf []byte) []float32 {
	out := make([]float32, len(buf)/2)
	scale := float32(math.Pow(2, 15))
	for i := 0; i < len(out); i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(buf[i*2:]))) / scale
	}
	return out
}
