#!/usr/bin/env bash
set -euo pipefail

APP_ROOT="/opt/wire-pod"
DATA_ROOT="${WIREPOD_DATA_DIR:-/data}"
DEFAULT_SOURCE="${APP_ROOT}/docker/default-source.sh"

mkdir -p "${DATA_ROOT}"

link_dir() {
    local rel_path="$1"
    local src_path="${APP_ROOT}/${rel_path}"
    local dest_path="${DATA_ROOT}/${rel_path}"

    mkdir -p "$(dirname "${dest_path}")"

    if [ ! -d "${dest_path}" ]; then
        if [ -d "${src_path}" ]; then
            cp -a "${src_path}" "${dest_path}"
        else
            mkdir -p "${dest_path}"
        fi
    fi

    if [ -e "${src_path}" ] && [ ! -L "${src_path}" ]; then
        rm -rf "${src_path}"
    fi

    ln -sfn "${dest_path}" "${src_path}"
}

link_file() {
    local rel_path="$1"
    local src_path="${APP_ROOT}/${rel_path}"
    local dest_path="${DATA_ROOT}/${rel_path}"

    mkdir -p "$(dirname "${dest_path}")"

    if [ ! -e "${dest_path}" ]; then
        if [ -f "${src_path}" ]; then
            cp -a "${src_path}" "${dest_path}"
        else
            : >"${dest_path}"
        fi
    fi

    if [ -e "${src_path}" ] && [ ! -L "${src_path}" ]; then
        rm -f "${src_path}"
    fi

    ln -sfn "${dest_path}" "${src_path}"
}

link_file_with_default() {
    local rel_path="$1"
    local default_path="$2"
    local src_path="${APP_ROOT}/${rel_path}"
    local dest_path="${DATA_ROOT}/${rel_path}"

    mkdir -p "$(dirname "${dest_path}")"

    if [ ! -e "${dest_path}" ]; then
        if [ -n "${default_path}" ] && [ -f "${default_path}" ]; then
            cp -a "${default_path}" "${dest_path}"
        elif [ -f "${src_path}" ]; then
            cp -a "${src_path}" "${dest_path}"
        else
            : >"${dest_path}"
        fi
    fi

    if [ -e "${src_path}" ] && [ ! -L "${src_path}" ]; then
        rm -f "${src_path}"
    fi

    ln -sfn "${dest_path}" "${src_path}"
}

persist_directories() {
    link_dir certs
    link_dir stt
    link_dir vosk
    link_dir whisper.cpp
    link_dir sherpa-onnx
    link_dir vector-cloud/build
    link_dir chipper/jdocs
    link_dir chipper/plugins
    link_dir chipper/session-certs
}

persist_files() {
    link_file chipper/apiConfig.json
    link_file chipper/botConfig.json
    link_file chipper/customIntents.json
    link_file chipper/pico.key
    link_file chipper/useepod
    link_file_with_default chipper/source.sh "${DEFAULT_SOURCE}"
}

update_export() {
    local key="$1"
    local value="$2"
    local file_path="$3"

    local escaped
    escaped=$(printf '%s' "${value}" | sed 's/[\\&/]/\\&/g')

    if grep -q "^export ${key}=" "${file_path}"; then
        sed -i "s/^export ${key}=.*/export ${key}=\"${escaped}\"/" "${file_path}"
    else
        printf 'export %s="%s"\n' "${key}" "${value}" >>"${file_path}"
    fi
}

ensure_sherpa_model() {
    local model_root="${DATA_ROOT}/sherpa-onnx/models"
    local model_dir="${model_root}/sense-voice"
    local model_name="${SHERPA_MODEL_NAME:-sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17}"

    if [ -d "${model_dir}" ] && [ -n "$(ls -A "${model_dir}" 2>/dev/null)" ]; then
        echo "[entrypoint] sherpa model present at ${model_dir}"
        return 0
    fi

    echo "[entrypoint] downloading sherpa model ${model_name}..."
    mkdir -p "${model_root}"
    cd "${model_root}"
    rm -f model.tar.bz2

    local urls=(
        "https://gh-proxy.com/https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/${model_name}.tar.bz2"
        "https://kkgithub.com/k2-fsa/sherpa-onnx/releases/download/asr-models/${model_name}.tar.bz2"
        "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/${model_name}.tar.bz2"
    )

    for url in "${urls[@]}"; do
        echo "[entrypoint] trying ${url}"
        if curl -fSL --connect-timeout 30 --retry 1 -o model.tar.bz2 "${url}"; then
            echo "[entrypoint] downloaded model from ${url}"
            break
        fi
        rm -f model.tar.bz2
    done

    if [ ! -s model.tar.bz2 ]; then
        echo "[entrypoint] all sherpa model mirrors failed" >&2
        exit 1
    fi

    tar -xjf model.tar.bz2
    rm model.tar.bz2
    rm -rf "${model_dir}"
    mv "${model_name}" sense-voice
    echo "[entrypoint] sherpa model installed at ${model_dir}"
}

apply_env_overrides() {
    local source_file="${APP_ROOT}/chipper/source.sh"

    if [ -n "${WIREPOD_DEBUG_LOGGING:-}" ]; then
        update_export "DEBUG_LOGGING" "${WIREPOD_DEBUG_LOGGING}" "${source_file}"
    fi

    if [ -n "${WIREPOD_STT_SERVICE:-}" ]; then
        update_export "STT_SERVICE" "${WIREPOD_STT_SERVICE}" "${source_file}"
    fi

    if [ -n "${WIREPOD_STT_LANGUAGE:-}" ]; then
        update_export "STT_LANGUAGE" "${WIREPOD_STT_LANGUAGE}" "${source_file}"
    fi

    if [ -n "${WIREPOD_USE_INBUILT_BLE:-}" ]; then
        update_export "USE_INBUILT_BLE" "${WIREPOD_USE_INBUILT_BLE}" "${source_file}"
    fi

    if [ -n "${WIREPOD_PICOVOICE_APIKEY:-}" ]; then
        update_export "PICOVOICE_APIKEY" "${WIREPOD_PICOVOICE_APIKEY}" "${source_file}"
        printf '%s\n' "${WIREPOD_PICOVOICE_APIKEY}" >"${DATA_ROOT}/chipper/pico.key"
    fi
}

persist_directories
persist_files

ensure_sherpa_model

apply_env_overrides

exec "$@"
