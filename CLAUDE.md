# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository layout

This repo is two independent Go modules plus packaging:

- `chipper/` — the **wire-pod server** that runs on a host machine and serves Vector robots. This is what you almost always touch. Module: `github.com/kercre123/wire-pod/chipper` (Go 1.18, but built with Go 1.22 in CI / Docker).
- `vector-cloud/` — replacement `vic-cloud` and `vic-gateway` binaries that run **on the robot itself** (armv7 / vicos). Built only via the Docker armbuilder; rarely modified. Separate `go.mod`.
- `setup.sh`, `dockerfile`, `docker/`, `compose.yaml` — host setup and container packaging.

The two Go modules don't import each other. Don't add cross-module references.

## Common commands

### Native (host) build & run — chipper

`setup.sh` and `start.sh` are the supported entry points; they exist because each STT engine needs different cgo flags and asset paths.

```bash
sudo ./setup.sh        # one-time: installs deps (Go 1.22, libopus, vosk libs, etc.) and writes chipper/source.sh
sudo ./chipper/start.sh # builds + runs chipper using the STT engine recorded in chipper/source.sh
```

`start.sh` reads `STT_SERVICE` from `chipper/source.sh` (`vosk` | `whisper.cpp` | `coqui` | `leopard` | `rhino` | `houndify` | `whisper`) and dispatches to the matching `cmd/<engine>/main.go`. Each engine main is ~3 lines: it calls `initwirepod.StartFromProgramInit` with an `Init`, `STT`, and `Name` from the matching `pkg/wirepod/stt/<engine>` package — that's the seam to add a new STT backend.

Always built with `-tags nolibopusfile` (set by `start.sh` as `GOTAGS`). Add `inbuiltble` to that list when `USE_INBUILT_BLE=true`. Commit hash is injected via `-ldflags "-X .../vars.CommitSHA=..."`.

To build a single STT entrypoint by hand (mirroring `start.sh`'s vosk branch):

```bash
cd chipper
export CGO_CFLAGS="-I$HOME/.vosk/libvosk"
export CGO_LDFLAGS="-L$HOME/.vosk/libvosk -lvosk -ldl -lpthread"
export LD_LIBRARY_PATH="$HOME/.vosk/libvosk:$LD_LIBRARY_PATH"
go build -tags nolibopusfile -o chipper ./cmd/vosk
```

There is **no test suite** and no linter config — don't fabricate `go test` or `golangci-lint` instructions.

### Docker

```bash
docker compose up --build    # builds image from ./dockerfile, exposes 80/443/8080/8084, persists state in named volumes
```

The Dockerfile only builds the `vosk` variant (`./cmd/vosk`) and bakes in vosk libs. `docker/entrypoint.sh` symlinks state directories (`certs`, `chipper/jdocs`, `chipper/plugins`, `chipper/apiConfig.json`, `chipper/source.sh`, …) under `${WIREPOD_DATA_DIR}` (default `/data`) so volumes persist them, then execs `chipper/start.sh`. If you add a new file/dir that must survive container rebuilds, register it in `persist_directories`/`persist_files` in `docker/entrypoint.sh`.

`WIREPOD_*` env vars (e.g. `WIREPOD_STT_SERVICE`, `WIREPOD_PICOVOICE_APIKEY`) are translated into `export` lines inside `chipper/source.sh` by `apply_env_overrides`. Treat `chipper/source.sh` as the canonical runtime config — env vars only seed it.

### vector-cloud (on-robot binaries)

```bash
cd vector-cloud
make docker-builder   # one-time: builds the cross-compile image
make vic-cloud        # produces build/vic-cloud (armv7 vicos, upx-packed)
make vic-gateway
```

Outputs go in `vector-cloud/build/` and are scp'd to the robot. Built with `-tags nolibopusfile,vicos -linkmode internal -extldflags "-static"`.

### Plugins

User plugins live in `chipper/plugins/<name>/`, are built with `go build -buildmode=plugin -o ../<name>.so`, and dropped into `chipper/plugins/`. `pkg/wirepod/ttr/plugins.go` loads every `.so` from `./plugins` at startup and requires three exported symbols: `Utterances []string`, `Action func(transcribedText, botSerial, guid, target string) (intent, response string)`, `Name string`. See `chipper/plugins/whatdate/whatdate.go` for the canonical example.

## Architecture — voice request flow

Vector firmware speaks gRPC to wire-pod over TLS. The path of a voice request:

1. **TLS + cmux + gRPC** — `pkg/initwirepod/startserver.go` listens on `APIConfig.Server.Port` (typically 443). `cmux` splits HTTP/2 (gRPC) from HTTP/1. When `EPConfig` (escape-pod mode) is on, a second listener on 8084 is started for Vector firmware 2.0.1 compatibility. Three gRPC services are registered: `chipperpb` (intent), `jdocspb` (per-bot docs), `tokenpb` (auth).
2. **Server impls** — `pkg/servers/{chipper,jdocs,token}` are thin gRPC adapters. The chipper server's intent processors are `*preqs.Server` from `pkg/wirepod/preqs`, wired via `WithIntentProcessor` / `WithKnowledgeGraphProcessor` / `WithIntentGraphProcessor`.
3. **Speech request abstraction** — `pkg/vtt/{intent,intentgraph,knowledgegraph}.go` define request types; `pkg/wirepod/speechrequest` normalizes them (handles Opus stream framing) into a single `SpeechRequest`.
4. **STT** — `preqs.New(InitFunc, SttHandler, name)` stores the handler. `SttHandler` is type-switched: `func(SpeechRequest)(string, error)` for STT engines, or `func(SpeechRequest)(string, map[string]string, error)` for speech-to-intent (Rhino) which bypasses text matching. Each engine lives in `pkg/wirepod/stt/<engine>/`.
5. **Intent matching** — `ttr.ProcessTextAll` (in `pkg/wirepod/ttr`) matches transcribed text against `vars.IntentList` (loaded from `chipper/intent-data/<lang>.json` plus `chipper/customIntents.json`) and against loaded plugins. On match it calls `ttr.IntentPass` to send the intent back over gRPC.
6. **LLM fallback** — if nothing matches and `Knowledge.IntentGraph && Knowledge.Enable`, `ttr.StreamingKGSim` streams an LLM response (OpenAI-compatible endpoint configured via `APIConfig.Knowledge`) back to the bot. `ttr/kgsim*.go` handles streaming, voice synthesis, and interruption.
7. **Plugins** — `ttr.LoadPlugins()` runs once during `preqs.New`. Plugin matches happen alongside built-in intent matching.

## Configuration model

Two coexisting layers — both are runtime-mutable:

- **`chipper/source.sh`** — shell-level (selects STT engine + cgo paths). Must exist before `start.sh` runs. Created by `setup.sh` or `docker/entrypoint.sh`.
- **`chipper/apiConfig.json`** (`pkg/vars/config.go::APIConfig`) — feature config: weather provider, knowledge/LLM provider+key+model+prompt, STT language, escape-pod-vs-IP mode (`Server.EPConfig`), `PastInitialSetup`. Mutated live by the config web UI. `vars.WriteConfigToDisk()` persists changes; never edit the struct without keeping JSON tags in sync.

`vars.APIConfig.Server.EPConfig` is load-bearing: it switches between **escape pod mode** (uses pre-shipped certs at `chipper/epod/ep.crt|key`, opens port 8084 too, runs mDNS via `pkg/mdnshandler`) and **IP mode** (uses `../certs/cert.crt|key` generated per install). Most cert/path branching keys off this flag.

## Web servers (in addition to gRPC)

Started concurrently from `BeginWirepodSpecific`:

- **Config web UI** on `:8080` — `pkg/wirepod/config-ws/webserver.go`. Main thread; serves `chipper/webroot/` and the setup/admin API. Health endpoint: `GET /ok`.
- **SDK app** — `pkg/wirepod/sdkapp/server.go` started via `sdkWeb.BeginServer()`. Lets users drive bots from a browser (`webroot/sdkapp/`). Uses `fforchino/vector-go-sdk`.
- **Setup / first-run** — `pkg/wirepod/setup` (BLE pairing, ssh, cert install on the bot). `setup.html` / `initial.html` in webroot.

## Things to know before editing

- **Don't add a `.go` file to `chipper/cmd/<engine>/` and expect it to run** — `start.sh` only knows the engines listed in its `if/elif` chain and the Dockerfile only builds vosk. New engines need updates in both.
- **The Go module declares `go 1.18`** but CI / dockerfile build with Go 1.22. Keep code compatible with both.
- **Plugin ABI is unversioned** — changing the `Action` signature in `pkg/wirepod/ttr/plugins.go` silently breaks every user-built `.so` in `./plugins`. Don't.
- **Intent JSON files** in `chipper/intent-data/*.json` are per-language; the same intent name must exist across languages for a feature to work everywhere. `customIntents.json` is user-edited via the config UI.
- **`vector-cloud/` is largely a vendored fork** of Anki's open-sourced vic-cloud/vic-gateway. Treat it as a separate codebase; almost all wire-pod work happens in `chipper/`.
