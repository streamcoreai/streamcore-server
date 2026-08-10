[English](./quickstart.md) | **简体中文**

# 快速开始

在本地跑通一次实时语音会话所需的全部步骤，以及 Docker 与完全本地运行两种变体。

## 前置条件

使用 Docker：只需 Docker。本地开发没有 Compose 文件 —— Compose 仅用于 [`infrastructure/aws/ec2`](../infrastructure/aws/ec2/) 中的 EC2 部署。

本地开发：

- Go 1.25+ —— 与 [`go.mod`](../go.mod) 中的 `go` 指令一致；CI 与 Docker 构建使用同一工具链
- Node.js 20+ 与 npm
- Python 3.10+（用于 Python 插件或示例）
- Rust 1.87+（用于 Rust SDK 或示例）

## 运行媒体运行时

```bash
cp config.toml.example config.toml
# 在 config.toml 中填入你的服务商凭据

go run .
```

或使用 Docker：

```bash
docker build -t streamcore-server .
docker run --rm -p 8080:8080 -v "$(pwd)/config.toml:/config.toml:ro" streamcore-server
```

服务监听 `:8080`，客户端连接 `http://localhost:8080/whip`。

上面只发布了信令端口，本地浏览器客户端仅需这一个。如果你设置了 `server.public_ip` 与 `server.turn_secret`，内置的 STUN/TURN 服务（[`internal/turn`](../internal/turn/)）还会监听 UDP **与** TCP 3478，并在 UDP 50001–60000 上中转媒体，因此这些端口也必须可达：

```bash
docker run --rm \
  -p 8080:8080 \
  -p 3478:3478/udp -p 3478:3478/tcp \
  -p 50001-60000:50001-60000/udp \
  -v "$(pwd)/config.toml:/config.toml:ro" \
  streamcore-server
```

在 Linux 上，建议用 `--network host` 而不是发布这一整段端口：Docker 会为每个发布的端口启动一个用户态代理，一万个 UDP 端口会让容器启动变慢，并给每个中转包多加一跳。EC2 部署使用 `network_mode: host` 正是这个原因。

## 接入一个客户端

```bash
git clone https://github.com/streamcoreai/examples.git
cd examples/typescript
npm install
npm run dev
```

打开 [http://localhost:3000](http://localhost:3000)。它默认连接 `http://localhost:8080/whip`。

## 接入你自己的后端

StreamCore 的意义在于智能体归你。最快的路径是做一个调用你自己服务的工具 —— 你的后端在干活的同时，智能体还在继续说话。

```bash
mkdir -p plugins/plugins/orders-lookup
```

`plugins/plugins/orders-lookup/plugin.yaml`

```yaml
name: orders.lookup
description: Look up an order by ID in the company order system
version: 1
language: python
entrypoint: main.py
thinking_sound: true
parameters:
  type: object
  properties:
    order_id:
      type: string
      description: The customer's order ID
  required:
    - order_id
```

`plugins/plugins/orders-lookup/main.py`

```python
import os, requests
from streamcoreai_plugin import StreamCoreAIPlugin

plugin = StreamCoreAIPlugin()

@plugin.on_execute
def handle(params):
    r = requests.get(
        f"{os.environ['BACKEND_URL']}/orders/{params['order_id']}",
        timeout=10,
    )
    r.raise_for_status()
    order = r.json()
    return f"Order {order['id']} is {order['status']}, arriving {order['eta']}."

plugin.run()
```

重启服务。你的后端就已经成为一次实时语音会话的一部分，而它周围每一毫秒的媒体链路都由 StreamCore 负责。

如果你想掌控整段对话，而不只是一次工具调用，见[接入你自己的智能体](./bring-your-own-agent.zh-CN.md)。

## 完全本地运行，无需 API key

用 Ollama 跑 LLM、用 VibeVoice 跑 STT/TTS，全部在你自己的硬件上运行。

**1. 安装并启动 Ollama**

```bash
brew install ollama            # macOS；Linux 见 https://ollama.ai
ollama serve
ollama pull gpt-oss:20b
```

**2. 启动 VibeVoice 边车进程**

```bash
# Apple Silicon (MLX)
pip install mlx-audio numpy websockets fastapi uvicorn
# 或 Linux / CUDA
# pip install torch transformers librosa numpy websockets fastapi uvicorn

python external/vibeVoice/vibeVoiceAsr/server.py   # ws://127.0.0.1:8200
python external/vibeVoice/vibeVoiceTTS/server.py   # http://127.0.0.1:8300
```

**3. 配置并运行**

```toml
[stt]
provider = "vibevoice"

[llm]
provider = "ollama"

[tts]
provider = "vibevoice"

[ollama]
base_url = "http://localhost:11434"
model = "gpt-oss:20b"

[vibevoice]
asr_url = "ws://127.0.0.1:8200"
tts_url = "http://127.0.0.1:8300"
voice = "en-Emma_woman"
```

```bash
go run .
```

完全本地的实时语音，不依赖任何外部 API。模型与边车进程的细节见[服务商 → 本地 VibeVoice 配置](./providers.zh-CN.md#本地-vibevoice-配置)。

---

下一步：[配置参考](./configuration.zh-CN.md) · [服务商](./providers.zh-CN.md) · [智能体运行时](./agent-runtime.zh-CN.md)
