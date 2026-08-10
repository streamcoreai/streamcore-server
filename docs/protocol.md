# Protocol reference

## WHIP signaling

Signaling follows [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html).

| Step | Method | Path | Body | Response |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer (`application/sdp`) | `201 Created` with SDP answer, `Location: /whip/{sessionId}`, and `ETag` |
| 2 | `DELETE` | `/whip/{sessionId}` | none | `200 OK` |
| — | `OPTIONS` | `/whip` or `/whip/{sessionId}` | none | `204 No Content` with `Accept-Post: application/sdp` |

`POST /whip` is rate limited per client IP (30 sessions per minute). Over the limit the server returns `429 Too Many Requests` with a `Retry-After` header. Each POST builds a peer connection and gathers ICE, so the endpoint is throttled even when auth is disabled.

The client creates an SDP offer, gathers ICE candidates, and `POST`s it to `/whip`. The server creates a peer, gathers its own candidates, and returns the answer with a server-generated session ID. No trickle ICE, no persistent signaling socket.

This implementation aligns with the core WHIP flow: `POST` with `application/sdp`, `201 Created` with the answer, `Location` for the session URL, `ETag` for the ICE session, `DELETE` for teardown, `OPTIONS` with `Accept-Post`, and full ICE gathering on both sides. Audio is `sendrecv`, with a DataChannel for bidirectional events.

## Realtime events

The client must create a DataChannel labeled `events` before generating the offer. The server sends:

| Type | Payload | Description |
|------|---------|-------------|
| `transcript` | `{ "type": "transcript", "text": string, "final": boolean }` | User transcript updates |
| `response` | `{ "type": "response", "text": string }` | Streamed response text |
| `state` | `{ "type": "state", "state": "listening" \| "thinking" \| "speaking" }` | Agent turn state, for UI indicators |
| `timing` | `{ "type": "timing", "stage": string, "ms": number }` | Latency timings when `pipeline.debug = true` |

Timing stages today: `llm_first_token`, `tts_first_byte`.

Messages the client sends on the same channel are routed into the pipeline — currently used for camera image chunks consumed by the `vision.analyze` plugin.

## Auth

Set `server.jwt_secret` to require `Authorization: Bearer <jwt>` on `/whip`. When it is set, the server also exposes `POST /token`, which issues an HS256 token valid for one hour. Set `server.api_key` to require `Authorization: Bearer <api_key>` on `/token` itself, so only your backend can mint session tokens. Both are empty by default, which disables auth.
