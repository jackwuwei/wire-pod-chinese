# wire-pod-chinese

[English](#english) · 中文说明见下

这是 [kercre123/wire-pod](https://github.com/kercre123/wire-pod) 的 fork，目标是让 Anki / DDL **Vector 机器人说中文、听中文**。上游的所有功能保持不变，本 fork 只增量添加中文链路所需的语音识别、语音合成与若干中文适配修复，并持续合并上游 main。

## 相比上游多了什么

| 能力 | 说明 |
| --- | --- |
| **sherpa-onnx SenseVoice 语音识别** | 全本地离线 STT，单模型支持中/英/日/韩/粤。作为第 5 个 STT 引擎加入，不影响原有 vosk / whisper 等选项。 |
| **edge-tts 语音合成** | 调用微软 Edge 神经网络语音（免费、无需 API key），默认 `zh-CN-XiaoxiaoNeural`，中文发音自然度远高于 Vector 自带 TTS。 |
| **GPT-SoVITS 语音合成** | 接自建的 GPT-SoVITS HTTP 服务，可用克隆音色说话（例如训练一个 Vector 自己的中文音色）。 |
| **边合成边播放** | LLM 回答按句流式送入 TTS，下一句在当前句播放时就已开始合成，消除句间 1–3 秒的网络等待。 |
| **中文断句** | 识别 `。？！` 等全角标点（上游只认 ASCII 标点，导致纯中文回答会憋到流结束才整段播放）；`...` 作为整体处理；结尾无标点时也会补 flush。 |
| **LLM 按识别语言回答** | system prompt 自动追加"请始终用<语言>回答"，zh-CN / ko-KR 等不再收到英文回复。 |
| **中文意图匹配修复** | 单个汉字关键词（如 `是`、`不`）不再参与子串匹配——中文没有词边界，这类字会出现在几乎任何句子里并抢掉 LLM 兜底；完整匹配仍然生效。 |
| **国内网络友好的 Docker 镜像** | 构建与运行阶段使用清华 TUNA / goproxy.cn / gh-proxy 镜像源；镜像默认构建 sherpa-onnx 版本，默认语言 zh-CN；模型在首次启动时下载到持久卷而不是打进镜像。 |
| **容器内远程调试** | `compose.debug.yaml` + `scripts/debug/incontainer-debug.sh`，在容器里装 Go + dlv 并以 headless 模式监听 `:2345`。 |

## 安装

### 原生安装（Linux / macOS）

和上游一样走 `setup.sh`，在选择 STT 引擎时多了一项：

```bash
sudo ./setup.sh
# 5: Sherpa-Onnx SenseVoice (local, multilingual incl. zh/en/ja/ko/yue)
sudo ./chipper/start.sh
```

选择 5 之后，`setup.sh` 会自动下载 SenseVoice 模型到 `sherpa-onnx/models/sense-voice/`，并创建 `chipper/.venv` 装好 `edge-tts`。

### Docker

```bash
docker compose up --build
```

镜像默认即 `STT_SERVICE=sherpa-onnx`、`STT_LANGUAGE=zh-CN`。SenseVoice 模型在首次启动时由 `docker/entrypoint.sh` 下载到 `${WIREPOD_DATA_DIR}/sherpa-onnx/models/`（默认 `/data`，为命名卷），镜像重建不会重新下载。

## 配置

装完后打开配置页 `http://<主机>:8080`（端口可用 `WEBSERVER_PORT` 改），在 setup 页面里：

- **STT**：选 `sherpa-onnx`，语言可选 `zh-CN` / `en-US` / `ko-KR`（SenseVoice 是单一多语模型，这里只列出有对应 `intent-data/*.json` 的语言）。
- **TTS provider**：留空为上游的 Auto（Vector 自带音色；LLM 为 OpenAI 时非英语走 OpenAI TTS），其余可选 `vector` / `openai` / `edge-tts` / `gpt-sovits`。中文建议 `edge-tts`。
- **edge-tts**：从下拉里选音色，或填任意 edge-tts 支持的 voice 名。
- **gpt-sovits**：填服务地址（默认 `http://127.0.0.1:8020/tts`）、目标语言、可选的 `ref_audio_path` / `ref_text` / `ref_lang`，以及增益和峰值归一化参数。留空则使用服务端默认值。

对应的 `chipper/apiConfig.json` 字段：`tts_provider`、`edge_tts_voice`、`sovits_url`、`sovits_lang`、`sovits_ref_audio`、`sovits_ref_text`、`sovits_ref_lang`、`sovits_gain_db`、`sovits_peak_dbfs`。

### GPT-SoVITS 服务端约定

wire-pod 向 `sovits_url` POST 一个 JSON（`text` / `lang` / `ref_audio_path` / `ref_text` / `ref_lang` / `gain_db` / `target_peak_dbfs`），期望返回 **32kHz 单声道 PCM-16 WAV**，wire-pod 会重采样到 16kHz 再推给机器人。任何满足该约定的 FastAPI 包装都可以直接用。

### 环境变量

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `SHERPA_MODEL_DIR` | `sense-voice` | 模型目录名（相对 `sherpa-onnx/models/`） |
| `SHERPA_LANGUAGE` | 由 STT 语言推导 | 强制 SenseVoice 识别语言（`zh`/`en`/`ja`/`ko`/`yue`/`auto`） |
| `WIREPOD_EDGETTS_PYTHON` | `./.venv/bin/python3` | edge-tts 所在的 python 解释器 |
| `WIREPOD_DEBUG_SRC` | `./wire-pod-debug` | `compose.debug.yaml` 里源码 bind mount 的宿主路径 |

## 安装与使用文档

机器人侧的证书安装、配网、故障排查等和上游完全一致，请参考上游 wiki：[Installation guide](https://github.com/kercre123/wire-pod/wiki/Installation) · [Wiki](https://github.com/kercre123/wire-pod/wiki)

---

## English

A fork of [kercre123/wire-pod](https://github.com/kercre123/wire-pod) that makes Vector speak and understand **Chinese**. Everything upstream still works; this fork only adds:

- **sherpa-onnx SenseVoice STT** — fully local, one model covering zh/en/ja/ko/yue (`setup.sh` option 5, or `./cmd/sherpa-onnx`).
- **edge-tts TTS provider** — free Microsoft Edge neural voices via the `edge-tts` CLI in `chipper/.venv`.
- **GPT-SoVITS TTS provider** — POSTs to a self-hosted GPT-SoVITS HTTP server for cloned voices.
- **Synthesis/playback pipeline** — the next sentence is synthesized while the current one plays, removing the per-sentence network gap.
- **CJK sentence splitting** — recognizes `。？！`, handles `...`, and flushes a trailing fragment with no terminal punctuation.
- **Language-aware LLM prompt** — the model is told to answer in the configured STT language.
- **CJK intent-matching fix** — single-ideograph keyphrases no longer substring-match and shadow the LLM fallback.
- **China-friendly Docker build** — TUNA / goproxy.cn / gh-proxy mirrors, sherpa-onnx by default, model fetched at first boot into the data volume.

See the Chinese section above for setup and configuration details. Robot-side setup (certs, pairing, troubleshooting) is unchanged — follow the [upstream wiki](https://github.com/kercre123/wire-pod/wiki).

## Credits

上游及其贡献者 / Upstream and its contributors:

- [kercre123](https://github.com/kercre123) 及 wire-pod 的全部贡献者
- [Digital Dream Labs](https://github.com/digital-dream-labs) for open sourcing chipper and escape pod
- [dietb](https://github.com/dietb) for rewriting chipper, [fforchino](https://github.com/fforchino) for localization and multilanguage, [xanathon](https://github.com/xanathon) for web interface help, [bliteknight](https://github.com/bliteknight) for pre-setup Linux boxes
- [k2-fsa/sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) 与 SenseVoice 模型
- [rany2/edge-tts](https://github.com/rany2/edge-tts)、[RVC-Boss/GPT-SoVITS](https://github.com/RVC-Boss/GPT-SoVITS)
