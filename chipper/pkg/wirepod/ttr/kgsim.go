package wirepod_ttr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/sashabaranov/go-openai"
)

func GetChat(esn string) vars.RememberedChat {
	for _, chat := range vars.RememberedChats {
		if chat.ESN == esn {
			return chat
		}
	}
	return vars.RememberedChat{
		ESN: esn,
	}
}

func PlaceChat(chat vars.RememberedChat) {
	for i, achat := range vars.RememberedChats {
		if achat.ESN == chat.ESN {
			vars.RememberedChats[i] = chat
			return
		}
	}
	vars.RememberedChats = append(vars.RememberedChats, chat)
}

// remember last 16 lines of chat
func Remember(user, ai openai.ChatCompletionMessage, esn string) {
	chatAppend := []openai.ChatCompletionMessage{
		user,
		ai,
	}
	currentChat := GetChat(esn)
	if len(currentChat.Chats) == 16 {
		var newChat vars.RememberedChat
		newChat.ESN = currentChat.ESN
		for i, chat := range currentChat.Chats {
			if i < 2 {
				continue
			}
			newChat.Chats = append(newChat.Chats, chat)
		}
		currentChat = newChat
	}
	currentChat.ESN = esn
	currentChat.Chats = append(currentChat.Chats, chatAppend...)
	PlaceChat(currentChat)
}

func isMn(r rune) bool {
	// Remove the characters that are not related to Vietnamese.
	// Retain the tonal marks and diacritics such as the circumflex, ơ, and ư in Vietnamese.
	keepMarks := []rune{
		'\u0300', // Dấu huyền
		'\u0301', // Dấu sắc
		'\u0303', // Dấu ngã
		'\u0309', // Dấu hỏi
		'\u0323', // Dấu nặng
		'\u0302', // Dấu mũ (â, ê, ô)
		'\u031B', // Dấu ơ và ư
		'\u0306', // Dấu trầm
	}
	if unicode.Is(unicode.Mn, r) {
		for _, mark := range keepMarks {
			if r == mark {
				return false
			}
		}
		return true
	}
	return false
}

func removeSpecialCharacters(str string) string {

	// these two lines create a transformation that decomposes characters, removes non-spacing marks (like diacritics), and then recomposes the characters, effectively removing special characters
	t := transform.Chain(norm.NFD, transform.RemoveFunc(isMn), norm.NFC)
	result, _, _ := transform.String(t, str)

	// Define the regular expression to match special characters
	re := regexp.MustCompile(`[&^*#@]`)

	// Replace special characters with an empty string
	result = removeEmojis(re.ReplaceAllString(result, ""))

	// Replace special characters with ASCII
	// * COPY/PASTE TO ADD MORE CHARACTERS:
	//   result = strings.ReplaceAll(result, "", "")
	result = strings.ReplaceAll(result, "‘", "'")
	result = strings.ReplaceAll(result, "’", "'")
	result = strings.ReplaceAll(result, "“", "\"")
	result = strings.ReplaceAll(result, "”", "\"")
	result = strings.ReplaceAll(result, "—", "-")
	result = strings.ReplaceAll(result, "–", "-")
	result = strings.ReplaceAll(result, "…", "...")
	result = strings.ReplaceAll(result, "\u00A0", " ")
	result = strings.ReplaceAll(result, "•", "*")
	result = strings.ReplaceAll(result, "¼", "1/4")
	result = strings.ReplaceAll(result, "½", "1/2")
	result = strings.ReplaceAll(result, "¾", "3/4")
	result = strings.ReplaceAll(result, "×", "x")
	result = strings.ReplaceAll(result, "÷", "/")
	result = strings.ReplaceAll(result, "ç", "c")
	result = strings.ReplaceAll(result, "©", "(c)")
	result = strings.ReplaceAll(result, "®", "(r)")
	result = strings.ReplaceAll(result, "™", "(tm)")
	result = strings.ReplaceAll(result, "@", "(a)")
	result = strings.ReplaceAll(result, " AI ", " A. I. ")
	return result
}

// stripThinkBlocks returns a stateful filter that removes <think>...</think>
// reasoning blocks from a streaming text source. It buffers a few trailing
// characters between calls so a tag straddling a chunk boundary is still
// detected. Pass final=true once the stream ends to flush any held-back text.
func stripThinkBlocks() func(chunk string, final bool) string {
	const openTag = "<think>"
	const closeTag = "</think>"
	var inThink bool
	var buf string
	return func(chunk string, final bool) string {
		buf += chunk
		var out strings.Builder
		for {
			if inThink {
				idx := strings.Index(buf, closeTag)
				if idx >= 0 {
					buf = buf[idx+len(closeTag):]
					inThink = false
					continue
				}
				if final {
					buf = ""
					return out.String()
				}
				if keep := len(closeTag) - 1; len(buf) > keep {
					buf = buf[len(buf)-keep:]
				}
				return out.String()
			}
			idx := strings.Index(buf, openTag)
			if idx >= 0 {
				out.WriteString(buf[:idx])
				buf = buf[idx+len(openTag):]
				inThink = true
				continue
			}
			if final {
				out.WriteString(buf)
				buf = ""
				return out.String()
			}
			if keep := len(openTag) - 1; len(buf) > keep {
				out.WriteString(buf[:len(buf)-keep])
				buf = buf[len(buf)-keep:]
			}
			return out.String()
		}
	}
}

// hasReadableContent reports whether s contains any character a TTS engine
// would actually pronounce. Inputs that are purely punctuation/whitespace
// (e.g. a stray '"' that the LLM wrapped around its whole response, or a
// standalone "..." chunk produced when consecutive ellipses arrive) cause
// edge-tts to return NoAudioReceived, so they should be filtered upstream.
func hasReadableContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// findSentenceEnd returns the byte index immediately after the first complete
// sentence terminator in s, or -1 if no terminator is found yet (caller should
// wait for more streamed text). Recognized terminators:
//   - 。 ？ ！ : always
//   - ?  !     : always (consecutive runs like "?!" or "!!" are kept together)
//   - ...      : as a unit (treated as a single ellipsis terminator)
//   - .        : only when prev rune is not a digit AND next rune is whitespace
//                or end-of-string. Bare '.' at the buffer tail returns -1 so
//                the caller can wait one more rune to disambiguate "1." from
//                "end."  (Same lookahead applies to ".." which could become
//                "..." or just "..X").
func findSentenceEnd(s string) int {
	type rp struct {
		r        rune
		startPos int // byte index of this rune in s
		endPos   int // byte index immediately after this rune
	}
	var rs []rp
	for i, r := range s {
		size := len(string(r))
		rs = append(rs, rp{r: r, startPos: i, endPos: i + size})
	}
	for i := 0; i < len(rs); i++ {
		switch rs[i].r {
		case '。', '？', '！':
			return rs[i].endPos
		case '?', '!':
			j := i
			for j+1 < len(rs) && (rs[j+1].r == '?' || rs[j+1].r == '!') {
				j++
			}
			return rs[j].endPos
		case '.':
			// "..." as a unit
			if i+2 < len(rs) && rs[i+1].r == '.' && rs[i+2].r == '.' {
				return rs[i+2].endPos
			}
			// ".." at tail: could become "..." once one more rune arrives
			if i+1 < len(rs) && rs[i+1].r == '.' && i+2 == len(rs) {
				return -1
			}
			// bare '.' at tail: need lookahead to know if it's end-of-sentence
			// or "1." / "v1.0" pattern
			if i+1 == len(rs) {
				return -1
			}
			next := rs[i+1].r
			// followed by alphanumeric → likely decimal/abbrev/domain, skip
			if (next >= '0' && next <= '9') || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') {
				continue
			}
			// preceded by a digit → numbered list ("1. ") or trailing decimal,
			// skip
			if i > 0 {
				if prev := rs[i-1].r; prev >= '0' && prev <= '9' {
					continue
				}
			}
			return rs[i].endPos
		}
	}
	return -1
}

func removeEmojis(input string) string {
	// a mess, but it works!
	re := regexp.MustCompile(`[\x{1F600}-\x{1F64F}]|[\x{1F300}-\x{1F5FF}]|[\x{1F680}-\x{1F6FF}]|[\x{1F1E0}-\x{1F1FF}]|[\x{2600}-\x{26FF}]|[\x{2700}-\x{27BF}]|[\x{1F900}-\x{1F9FF}]|[\x{1F004}]|[\x{1F0CF}]|[\x{1F18E}]|[\x{1F191}-\x{1F251}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]`)
	result := re.ReplaceAllString(input, "")
	return result
}

func CreateAIReq(transcribedText, esn string, gpt3tryagain, isKG bool) openai.ChatCompletionRequest {
	defaultPrompt := "You are a helpful, animated robot called Vector. Keep the response concise yet informative."

	var nChat []openai.ChatCompletionMessage

	smsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleSystem,
	}
	if strings.TrimSpace(vars.APIConfig.Knowledge.OpenAIPrompt) != "" {
		smsg.Content = strings.TrimSpace(vars.APIConfig.Knowledge.OpenAIPrompt)
	} else {
		smsg.Content = defaultPrompt
	}

	var model string

	if gpt3tryagain {
		model = openai.GPT3Dot5Turbo
	} else if vars.APIConfig.Knowledge.Provider == "openai" {
		model = openai.GPT4oMini
		logger.Println("Using " + model)
	} else {
		logger.Println("Using " + vars.APIConfig.Knowledge.Model)
		model = vars.APIConfig.Knowledge.Model
	}

	smsg.Content = CreatePrompt(smsg.Content, model, isKG)

	nChat = append(nChat, smsg)
	if vars.APIConfig.Knowledge.SaveChat {
		rchat := GetChat(esn)
		logger.Println("Using remembered chats, length of " + fmt.Sprint(len(rchat.Chats)) + " messages")
		nChat = append(nChat, rchat.Chats...)
	}
	nChat = append(nChat, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: transcribedText,
	})

	aireq := openai.ChatCompletionRequest{
		Model:            model,
		MaxTokens:        2048,
		Temperature:      1,
		TopP:             1,
		FrequencyPenalty: 0,
		PresencePenalty:  0,
		Messages:         nChat,
		Stream:           true,
	}
	return aireq
}

func StreamingKGSim(req interface{}, esn string, transcribedText string, isKG bool) (string, error) {
	start := make(chan bool)
	stop := make(chan bool)
	stopStop := make(chan bool)
	kgReadyToAnswer := make(chan bool)
	kgStopLooping := false
	ctx := context.Background()
	matched := false
	var robot *vector.Vector
	var guid string
	var target string
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if matched {
		var err error
		robot, err = vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
		if err != nil {
			return err.Error(), err
		}
	}
	_, err := robot.Conn.BatteryState(context.Background(), &vectorpb.BatteryStateRequest{})
	if err != nil {
		return "", err
	}
	if isKG {
		BControl(robot, ctx, start, stop)
		go func() {
			for {
				if kgStopLooping {
					kgReadyToAnswer <- true
					break
				}
				robot.Conn.PlayAnimation(ctx, &vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: "anim_knowledgegraph_searching_01",
					},
					Loops: 1,
				})
				time.Sleep(time.Second / 3)
			}
		}()
	}
	var fullRespText string
	var fullfullRespText string
	var fullRespSlice []string
	var isDone bool
	var c *openai.Client
	switch vars.APIConfig.Knowledge.Provider {
	case "together":
		if vars.APIConfig.Knowledge.Model == "" {
			vars.APIConfig.Knowledge.Model = "meta-llama/Llama-3-70b-chat-hf"
			vars.WriteConfigToDisk()
		}
		conf := openai.DefaultConfig(vars.APIConfig.Knowledge.Key)
		conf.BaseURL = "https://api.together.xyz/v1"
		c = openai.NewClientWithConfig(conf)
	case "custom":
		conf := openai.DefaultConfig(vars.APIConfig.Knowledge.Key)
		conf.BaseURL = vars.APIConfig.Knowledge.Endpoint
		c = openai.NewClientWithConfig(conf)
	case "openai":
		c = openai.NewClient(vars.APIConfig.Knowledge.Key)
	}
	speakReady := make(chan string)
	successIntent := make(chan bool)

	aireq := CreateAIReq(transcribedText, esn, false, isKG)

	stream, err := c.CreateChatCompletionStream(ctx, aireq)
	if err != nil {
		log.Printf("Error creating chat completion stream: %v", err)
		if strings.Contains(err.Error(), "does not exist") && vars.APIConfig.Knowledge.Provider == "openai" {
			logger.Println("GPT-4 model cannot be accessed with this API key. You likely need to add more than $5 dollars of funds to your OpenAI account.")
			logger.LogUI("GPT-4 model cannot be accessed with this API key. You likely need to add more than $5 dollars of funds to your OpenAI account.")
			aireq := CreateAIReq(transcribedText, esn, true, isKG)
			logger.Println("Falling back to " + aireq.Model)
			logger.LogUI("Falling back to " + aireq.Model)
			stream, err = c.CreateChatCompletionStream(ctx, aireq)
			if err != nil {
				logger.Println("OpenAI still not returning a response even after falling back. Erroring.")
				return "", err
			}
		} else {
			if isKG {
				kgStopLooping = true
				for range kgReadyToAnswer {
					break
				}
				stop <- true
				time.Sleep(time.Second / 3)
				KGSim(esn, "There was an error getting data from the L. L. M.")
			}
			return "", err
		}
	}
	nChat := aireq.Messages
	nChat = append(nChat, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
	})
	fmt.Println("LLM stream response: ")
	filterThink := stripThinkBlocks()
	go func() {
		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				if tail := filterThink("", true); tail != "" {
					fullfullRespText = fullfullRespText + tail
					fullRespText = fullRespText + tail
				}
				// Flush the trailing remainder that never hit a sentence-splitting
				// punctuation mark (short answers, CJK-only punctuation like 。？！,
				// or LLMs that don't end with a period).
				if remainder := strings.TrimSpace(fullRespText); remainder != "" {
					fullRespSlice = append(fullRespSlice, remainder)
					fullRespText = ""
					select {
					case successIntent <- true:
					default:
					}
					select {
					case speakReady <- remainder:
					default:
					}
				}
				if len(fullRespSlice) == 0 {
					logger.Println("LLM returned no response")
					successIntent <- false
					if isKG {
						kgStopLooping = true
						for range kgReadyToAnswer {
							break
						}
						stop <- true
						time.Sleep(time.Second / 3)
						KGSim(esn, "There was an error getting data from the L. L. M.")
					}
					break
				}
				isDone = true
				newStr := fullRespSlice[0]
				for i, str := range fullRespSlice {
					if i == 0 {
						continue
					}
					newStr = newStr + " " + str
				}
				if vars.APIConfig.Knowledge.SaveChat {
					Remember(openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: transcribedText,
					},
						openai.ChatCompletionMessage{
							Role:    openai.ChatMessageRoleAssistant,
							Content: newStr,
						},
						esn)
				}
				logger.LogUI("LLM response for " + esn + ": " + newStr)
				logger.Println("LLM stream finished")
				return
			}

			if err != nil {
				logger.Println("Stream error: " + err.Error())
				return
			}

			if len(response.Choices) == 0 {
				logger.Println("Empty response")
				return
			}

			delta := filterThink(removeSpecialCharacters(response.Choices[0].Delta.Content), false)
			if delta == "" {
				continue
			}
			fullfullRespText = fullfullRespText + delta
			fullRespText = fullRespText + delta
			// Drain every complete sentence currently in the buffer. A single
			// delta can contain multiple sentence terminators, so loop until
			// findSentenceEnd reports nothing left to flush.
			for {
				end := findSentenceEnd(fullRespText)
				if end < 0 {
					break
				}
				sentence := strings.TrimSpace(fullRespText[:end])
				fullRespText = fullRespText[end:]
				if sentence == "" {
					continue
				}
				fullRespSlice = append(fullRespSlice, sentence)
				select {
				case successIntent <- true:
				default:
				}
				select {
				case speakReady <- sentence:
				default:
				}
			}
		}
	}()
	for is := range successIntent {
		if is {
			if !isKG {
				IntentPass(req, "intent_greeting_hello", transcribedText, map[string]string{}, false)
			}
			break
		} else {
			return "", errors.New("llm returned no response")
		}
	}
	time.Sleep(time.Millisecond * 200)
	if !isKG {
		BControl(robot, ctx, start, stop)
	}
	interrupted := false
	go func() {
		interrupted = InterruptKGSimWhenTouchedOrWaked(robot, stop, stopStop)
	}()
	var TTSLoopAnimation string
	var TTSGetinAnimation string
	if isKG {
		TTSLoopAnimation = "anim_knowledgegraph_answer_01"
		TTSGetinAnimation = "anim_knowledgegraph_searching_getout_01"
	} else {
		TTSLoopAnimation = "anim_tts_loop_02"
		TTSGetinAnimation = "anim_getin_tts_01"
	}

	var stopTTSLoop bool
	TTSLoopStopped := make(chan bool)
	for range start {
		if isKG {
			kgStopLooping = true
			for range kgReadyToAnswer {
				break
			}
		} else {
			time.Sleep(time.Millisecond * 300)
		}
		robot.Conn.PlayAnimation(
			ctx,
			&vectorpb.PlayAnimationRequest{
				Animation: &vectorpb.Animation{
					Name: TTSGetinAnimation,
				},
				Loops: 1,
			},
		)
		if !vars.APIConfig.Knowledge.CommandsEnable {
			go func() {
				for {
					if stopTTSLoop {
						TTSLoopStopped <- true
						break
					}
					robot.Conn.PlayAnimation(
						ctx,
						&vectorpb.PlayAnimationRequest{
							Animation: &vectorpb.Animation{
								Name: TTSLoopAnimation,
							},
							Loops: 1,
						},
					)
				}
			}()
		}
		var disconnect bool
		ttsProv := vars.APIConfig.Knowledge.TTSProvider
		if ttsProv == "edge-tts" || ttsProv == "gpt-sovits" {
			synth := synthEdgeTTS
			if ttsProv == "gpt-sovits" {
				synth = synthSoVITS
			}
			disconnect = runStreamingTTSPipeline(synth, robot, &fullRespSlice, &fullRespText, &isDone, &interrupted, speakReady, stopStop, nChat)
		} else {
			numInResp := 0
			for {
				respSlice := fullRespSlice
				if len(respSlice)-1 < numInResp {
					if !isDone {
						logger.Println("Waiting for more content from LLM...")
						for range speakReady {
							respSlice = fullRespSlice
							break
						}
					} else {
						break
					}
				}
				if interrupted {
					break
				}
				logger.Println(respSlice[numInResp])
				acts := GetActionsFromString(respSlice[numInResp])
				nChat[len(nChat)-1].Content = fullRespText
				disconnect = PerformActions(nChat, acts, robot, stopStop)
				if disconnect {
					break
				}
				numInResp = numInResp + 1
			}
		}
		if !vars.APIConfig.Knowledge.CommandsEnable {
			stopTTSLoop = true
			for range TTSLoopStopped {
				break
			}
		}
		time.Sleep(time.Millisecond * 100)
		// if isKG {
		// 	robot.Conn.PlayAnimation(
		// 		ctx,
		// 		&vectorpb.PlayAnimationRequest{
		// 			Animation: &vectorpb.Animation{
		// 				Name: "anim_knowledgegraph_success_01",
		// 			},
		// 			Loops: 1,
		// 		},
		// 	)
		// 	time.Sleep(time.Millisecond * 3300)
		// }
		if !interrupted {
			stopStop <- true
			stop <- true
		}
	}
	return "", nil
}

// runStreamingTTSPipeline drives the playback loop for any synth-then-stream
// TTS provider (edge-tts, gpt-sovits) with a single-producer/single-consumer
// pipeline so that the next sentence is being synthesized while the current
// one is playing on the robot.
//
// It mirrors the synchronous PerformActions branch in side effects (animation
// dispatch, getImage / newRequest disconnect signaling, behavior queue wait)
// but moves the slow synth call off the playback critical path.
//
// The `synth` function takes a sentence and returns 16kHz mono PCM chunks
// ready for ExternalAudioStreamPlayback.
func runStreamingTTSPipeline(
	synth func(string) ([][]byte, error),
	robot *vector.Vector,
	fullRespSlice *[]string,
	fullRespText *string,
	isDone *bool,
	interrupted *bool,
	speakReady chan string,
	stopStop chan bool,
	nChat []openai.ChatCompletionMessage,
) bool {
	type prepared struct {
		act      RobotAction
		audio    [][]byte
		synthErr error
	}
	prepChan := make(chan prepared, 2)

	go func() {
		defer close(prepChan)
		numInResp := 0
		for {
			respSlice := *fullRespSlice
			if len(respSlice)-1 < numInResp {
				if !*isDone {
					logger.Println("Waiting for more content from LLM...")
					for range speakReady {
						break
					}
					continue
				}
				return
			}
			if *interrupted {
				return
			}
			chunk := respSlice[numInResp]
			logger.Println(chunk)
			for _, a := range GetActionsFromString(chunk) {
				if *interrupted {
					return
				}
				if a.Action == ActionSayText {
					if !hasReadableContent(a.Parameter) {
						continue
					}
					audio, err := synth(a.Parameter)
					prepChan <- prepared{act: a, audio: audio, synthErr: err}
				} else {
					prepChan <- prepared{act: a}
				}
			}
			numInResp++
		}
	}()

	disconnect := false
	for p := range prepChan {
		if *interrupted {
			break
		}
		nChat[len(nChat)-1].Content = *fullRespText
		switch {
		case p.act.Action == ActionSayText:
			if p.synthErr != nil {
				logger.Println("tts synth failed (skipping): " + p.synthErr.Error())
				continue
			}
			playEdgeTTSAudio(robot, p.audio)
		case p.act.Action == ActionPlayAnimation:
			DoPlayAnimation(p.act.Parameter, robot)
		case p.act.Action == ActionPlayAnimationWI:
			// In the pipelined path PlayAnimationWI must run synchronously:
			// Vector cancels in-flight ExternalAudioStreamPlayback when a new
			// animation starts, so the original fire-and-forget goroutine
			// races against the next sentence's playback and silently kills
			// it. Without the synthesis-latency buffer of the synchronous
			// path, that race is reliably lost.
			DoPlayAnimation(p.act.Parameter, robot)
		case p.act.Action == ActionNewRequest:
			go DoNewRequest(robot)
			disconnect = true
		case p.act.Action == ActionGetImage:
			DoGetImage(nChat, p.act.Parameter, robot, stopStop)
			disconnect = true
		case p.act.Action == ActionPlaySound:
			DoPlaySound(p.act.Parameter, robot)
		}
		if disconnect {
			break
		}
	}
	WaitForAnim_Queue(robot.Cfg.SerialNo)
	return disconnect
}

func KGSim(esn string, textToSay string) error {
	ctx := context.Background()
	matched := false
	var robot *vector.Vector
	var guid string
	var target string
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if matched {
		var err error
		robot, err = vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
		if err != nil {
			return err
		}
	}
	controlRequest := &vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
			ControlRequest: &vectorpb.ControlRequest{
				Priority: vectorpb.ControlRequest_OVERRIDE_BEHAVIORS,
			},
		},
	}
	go func() {
		start := make(chan bool)
		stop := make(chan bool)

		go func() {
			// * begin - modified from official vector-go-sdk
			r, err := robot.Conn.BehaviorControl(
				ctx,
			)
			if err != nil {
				log.Println(err)
				return
			}

			if err := r.Send(controlRequest); err != nil {
				log.Println(err)
				return
			}

			for {
				ctrlresp, err := r.Recv()
				if err != nil {
					log.Println(err)
					return
				}
				if ctrlresp.GetControlGrantedResponse() != nil {
					start <- true
					break
				}
			}

			for {
				select {
				case <-stop:
					logger.Println("KGSim: releasing behavior control (interrupt)")
					if err := r.Send(
						&vectorpb.BehaviorControlRequest{
							RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
								ControlRelease: &vectorpb.ControlRelease{},
							},
						},
					); err != nil {
						log.Println(err)
						return
					}
					return
				default:
					continue
				}
			}
			// * end - modified from official vector-go-sdk
		}()

		var stopTTSLoop bool
		var TTSLoopStopped bool
		for range start {
			time.Sleep(time.Millisecond * 300)
			robot.Conn.PlayAnimation(
				ctx,
				&vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: "anim_getin_tts_01",
					},
					Loops: 1,
				},
			)
			go func() {
				for {
					if stopTTSLoop {
						TTSLoopStopped = true
						break
					}
					robot.Conn.PlayAnimation(
						ctx,
						&vectorpb.PlayAnimationRequest{
							Animation: &vectorpb.Animation{
								Name: "anim_tts_loop_02",
							},
							Loops: 1,
						},
					)
				}
			}()
			textToSaySplit := strings.Split(textToSay, ". ")
			for _, str := range textToSaySplit {
				if err := DoSayText(str+".", robot); err != nil {
					logger.Println("KG SayText error: " + err.Error())
					stop <- true
					break
				}
			}
			stopTTSLoop = true
			for {
				if TTSLoopStopped {
					break
				} else {
					time.Sleep(time.Millisecond * 10)
				}
			}
			time.Sleep(time.Millisecond * 100)
			//time.Sleep(time.Millisecond * 3300)
			stop <- true
		}
	}()
	return nil
}
