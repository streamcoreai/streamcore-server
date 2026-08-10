# Roadmap

Not built yet — listed here so the [capability tables](./capabilities.md) stay honest:

- **Horizontal scaling.** Session state is in-memory and single-process. Multi-instance deployments need sticky routing or external session coordination today.
- **Session reconnection.** There is no ICE restart or resume path; a dropped connection means a new session.
- **Metrics and observability.** `/health` and DataChannel timing events exist; there is no Prometheus/OpenTelemetry export.
- **HTTP agent endpoint.** A configurable OpenAI-compatible or webhook-style agent backend, so [bring-your-own-agent](./bring-your-own-agent.md) needs no Go code.
- **Persistent memory** across sessions.
- **Broader examples** proving the positioning: realtime translator, AI-hosted voice room, browser copilot, embedded device, SIP application, and a raw audio-processing app with no LLM at all.
- **Embedded client hardening.** The ESP32-S3 firmware in [`esp32`](https://github.com/streamcoreai/esp32) connects over WHIP but is not production-ready.
