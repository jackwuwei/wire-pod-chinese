# wire-pod-chinese

让 Anki / DDL **Vector 机器人说中文、听中文**的 [wire-pod](https://github.com/kercre123/wire-pod) 镜像。源码：[jackwuwei/wire-pod-chinese](https://github.com/jackwuwei/wire-pod-chinese)

支持 `linux/amd64` 和 `linux/arm64`（树莓派 4/5、ARM NAS 等）。

## 相比上游多了什么

- **sherpa-onnx SenseVoice 语音识别**：本地离线，一个模型覆盖中/英/日/韩/粤，镜像默认使用，默认语言 `zh-CN`
- **edge-tts 语音合成**：微软 Edge 神经网络语音，免费，不需要 API key
- **GPT-SoVITS 语音合成**：对接自建的 GPT-SoVITS 服务，可以用克隆的音色说话，配套镜像 [jackwuwei/vector-tts](https://hub.docker.com/r/jackwuwei/vector-tts)
- **LLM 回答边合成边播放**，支持中文断句，LLM 按识别出的语言回答

## 快速开始

Vector 固件只会连接 `escapepod.local` 的 443 端口，所以推荐在 Linux 上用 host 网络（mDNS 广播也需要它）：

```yaml
services:
  wire-pod:
    image: jackwuwei/wire-pod-chinese:latest
    container_name: wire-pod
    hostname: escapepod
    network_mode: host
    restart: unless-stopped
    volumes:
      - wire-pod-data:/data

volumes:
  wire-pod-data:
```

```bash
docker compose up -d
```

启动后打开 `http://<主机 IP>:8080` 完成设置，然后按上游的 [安装指南](https://github.com/kercre123/wire-pod/wiki/Installation) 配对 Vector。

首次启动会下载约 1GB 的 SenseVoice 模型到 `/data`，之后重建容器不会重复下载。

## 端口

| 端口 | 用途 |
| --- | --- |
| 443 | Vector 连接的 gRPC 服务（必需，不能改到别的端口） |
| 80 | 连通性检查 |
| 8080 | 配置网页 |
| 8084 | 2.0.1 固件兼容 |

宿主机的 80/443 被占用时（比如 NAS 管理界面），可以给容器单独分配一个局域网 IP（macvlan），或者换一台机器部署。

## 数据卷

`/data` 保存所有状态：配置（`apiConfig.json`、`source.sh`）、机器人数据（jdocs、证书）、SenseVoice 模型。升级镜像时保留这个卷即可。

## 环境变量

这些变量只在启动时写入 `/data/chipper/source.sh`，之后以该文件为准。

| 变量 | 说明 |
| --- | --- |
| `WIREPOD_STT_SERVICE` | STT 引擎，镜像只内置 `sherpa-onnx` |
| `WIREPOD_STT_LANGUAGE` | 识别语言，如 `zh-CN`、`en-US` |
| `WIREPOD_DEBUG_LOGGING` | `true` 或 `false` |
| `WIREPOD_DATA_DIR` | 数据目录，默认 `/data` |

## 使用 GPT-SoVITS 音色

先部署 [jackwuwei/vector-tts](https://hub.docker.com/r/jackwuwei/vector-tts)，然后在配置页把 TTS provider 选为 `gpt-sovits`，服务地址填 `http://<vector-tts 主机>:8020/tts`。

---

**English**: a fork of wire-pod that lets Vector speak and understand Chinese. It adds local SenseVoice STT, edge-tts and GPT-SoVITS TTS, and streams LLM replies sentence by sentence. Run it with `network_mode: host` so Vector can reach `escapepod.local:443`, then open `http://<host>:8080` to set it up.
