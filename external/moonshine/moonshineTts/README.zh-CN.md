# Moonshine TTS Server

[English](./README.md) | **简体中文**

使用 [Moonshine](https://moonshine.ai) 的文字转语音服务。`/v1/speak` 对齐 Deepgram 的同名接口 —— 文本放在 body、音频格式放在 query、边合成边把无头 PCM 写回 —— 因此播放可以从第一个短句就开始，而不必等整句合成完。

全部在本机运行。无需 API key，无需账号 —— 音色首次使用时下载并缓存。

## 音色

音色 id 带有声码器前缀：`kokoro_`、`piper_` 或 `zipvoice_`。`GET /v1/voices` 会列出本地已有的和目录中可下载的音色。

```bash
curl 'http://127.0.0.1:8310/v1/voices?language=en_us'
```

## 安装

```bash
pip install -r requirements.txt
```

## 运行

```bash
python server.py
# http://127.0.0.1:8310

python server.py --port 9000 --voice kokoro_am_adam
python server.py --language es --voice kokoro_ef_dora --preload
```

某个音色的首次请求会触发下载，可能较慢。`--preload` 把这个开销挪到启动时。

## 接口

### `POST /v1/speak`

| Query 参数 | 默认值 | 说明 |
|-------------|---------|-------------|
| `voice` | `--voice` | 目录中的音色 id |
| `language` | `--language` | 合成语言，例如 `en_us` |
| `encoding` | `linear16` | 仅支持 `linear16` |
| `sample_rate` | `16000` | 输出采样率；模型自身采样率会重采样到该值 |
| `container` | `none` | 仅支持 `none` |

body 即文本，可以是 `text/plain`，也可以是 Deepgram 的 `{"text": "..."}` JSON。响应是 `sample_rate` 下的原始 PCM（16 位有符号小端、单声道），按回复的每个片段分块流式返回。

```bash
curl -s -X POST 'http://127.0.0.1:8310/v1/speak?voice=kokoro_af_heart' \
  -H 'Content-Type: text/plain' \
  --data 'Hello from Moonshine.' --output speech.pcm
```

### `GET /v1/voices`

```json
{"language": "en_us", "present": ["kokoro_af_heart"], "downloadable": ["kokoro_af_bella", "..."]}
```

### `GET /health`

```json
{"status": "ok"}
```

## 选项

| 参数 | 默认值 | 说明 |
|------|---------|-------------|
| `--host` | `127.0.0.1` | 绑定地址 |
| `--port` | `8310` | 绑定端口 |
| `--language` | `en_us` | 默认合成语言 |
| `--voice` | `kokoro_af_heart` | 默认音色 id |
| `--preload` | 关闭 | 在启动时而非首次请求时加载默认音色 |
| `--log-level` | `INFO` | 日志级别 |

## 说明

一个合成器同一时间只能说一件事 —— 原生层会拒绝正在生成时发起的第二次生成 —— 所以请求是串行处理的。在 M 系列 Mac 上用 `kokoro_af_heart` 实测：首个分块约 130 ms，合成速度约为播放速度的 9 倍，对实时对话来说余量充足。

分块重采样时会沿用连续的小数读取位置，而不是逐块独立重采样，因此块与块的衔接处不会出现爆音。
