# StreamCore documentation

| Page | What's in it |
|------|--------------|
| [Quick start](./quickstart.md) | Prerequisites, Docker and TURN ports, connecting a client, wiring your backend behind a tool call, fully-local setup |
| [Capabilities](./capabilities.md) | What the runtime does today, supported endpoints, AI integrations |
| [Bring your own agent](./bring-your-own-agent.md) | The four ways to keep the intelligence in your own stack, including the `llm.Client` interface |
| [Agent runtime](./agent-runtime.md) | Optional built-in agent: plugins, skills, RAG, document ingestion |
| [Providers](./providers.md) | STT / LLM / TTS options, Grok speech-to-speech, MiniMax, local VibeVoice |
| [Configuration](./configuration.md) | Full annotated `config.toml` reference |
| [Protocol](./protocol.md) | WHIP signaling, DataChannel events, auth |
| [Architecture](./architecture.md) | Media flow, why Go, package layout |
| [Roadmap](./roadmap.md) | What is deliberately not built yet |

Deploying on AWS EC2: [`infrastructure/aws/ec2`](../infrastructure/aws/ec2/). Contributing: [CONTRIBUTING.md](../CONTRIBUTING.md). Security: [SECURITY.md](../SECURITY.md).
