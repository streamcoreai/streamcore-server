**English** | [简体中文](./protocol.zh-CN.md)

# Protocol reference

## WHIP signaling

Signaling follows [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html).

| Step | Method | Path | Body | Response |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer (`application/sdp`) | `201 Created` with SDP answer, `Location: /whip/{sessionId}`, `ETag`, and `Accept-Patch` |
| 2 | `PATCH` | `/whip/{sessionId}` | ICE restart fragment (`application/trickle-ice-sdpfrag`) | `200 OK` with the server's fragment and a new `ETag` |
| 3 | `DELETE` | `/whip/{sessionId}` | none | `200 OK` |
| — | `OPTIONS` | `/whip` or `/whip/{sessionId}` | none | `204 No Content` with `Accept-Post: application/sdp` and `Accept-Patch: application/trickle-ice-sdpfrag` |

`POST /whip` is rate limited per client IP (30 sessions per minute); `PATCH` has its own bucket (60 restarts per minute) so a client recovering from a flapping network never eats into its budget for opening new sessions. Over either limit the server returns `429 Too Many Requests` with a `Retry-After` header. Each request builds or re-gathers ICE, so both are throttled even when auth is disabled.

The client creates an SDP offer, gathers ICE candidates, and `POST`s it to `/whip`. The server creates a peer, gathers its own candidates, and returns the answer with a server-generated session ID. No trickle ICE, no persistent signaling socket.

This implementation aligns with the core WHIP flow: `POST` with `application/sdp`, `201 Created` with the answer, `Location` for the session URL, `ETag` for the ICE session, `PATCH` for ICE restart, `DELETE` for teardown, `OPTIONS` with `Accept-Post`, and full ICE gathering on both sides. Audio is `sendrecv`, with a DataChannel for bidirectional events.

### ICE restart

A transient network event — a phone moving between Wi-Fi and cellular, a laptop changing networks, a NAT rebinding after an idle gap — breaks connectivity without ending the call. Recovering by `POST`ing a fresh offer would allocate a new session, a new pipeline, and a new LLM client, so the conversation history and the rolling summary would be gone and the greeting would replay. `PATCH` recovers the *same* connection instead: new ICE credentials and candidates, but the same `PeerConnection`, the same DTLS association, the same tracks, and the same running pipeline.

The client re-gathers ICE (`pc.restartIce()` in a browser) and sends the resulting credentials and candidates to the session URL from the `Location` header:

```http
PATCH /whip/{sessionId} HTTP/1.1
Content-Type: application/trickle-ice-sdpfrag
If-Match: "<etag>"

a=ice-ufrag:ZjRk
a=ice-pwd:AYk4ZQlPQeZ1AyJEkxUFXA
m=audio 9 UDP/TLS/RTP/SAVPF 111
a=mid:0
a=candidate:1 1 udp 2130706431 198.51.100.7 51000 typ host
```

The server replies `200 OK` with its own fragment and a rotated `ETag`:

```http
HTTP/1.1 200 OK
Content-Type: application/trickle-ice-sdpfrag
ETag: "<new-etag>"

a=ice-ufrag:MInq
a=ice-pwd:9NAlFOwsD1owEQGZjnjqvSVU
m=audio 9 UDP/TLS/RTP/SAVPF 111
a=mid:0
a=candidate:1 1 udp 2130706431 198.51.100.1 39132 typ host
a=end-of-candidates
```

`If-Match` is optional but checked when present: it must carry the current `ETag` (or `*`), otherwise the server returns `412 Precondition Failed` with the current tag in the response. Other outcomes:

| Status | Meaning |
|--------|---------|
| `404 Not Found` | The session has been reaped or never existed — redial with `POST`. |
| `405 Method Not Allowed` | The fragment had candidates but no `ice-ufrag`/`ice-pwd`. Trickle ICE is optional per RFC 9725 §4.4.1 and is not implemented; gather fully, then patch. |
| `409 Conflict` | The session exists but has no negotiated peer to restart. |
| `415 Unsupported Media Type` | `Content-Type` was not `application/trickle-ice-sdpfrag`. |

The `disconnected` connection state is transient and never tears a peer down on its own — ICE recovers from it unaided, and Pion escalates to `failed` (which does tear down) after roughly 25 seconds if it does not. While disconnected the server emits a `connection` event on the DataChannel so the client can surface "reconnecting…", and another when it recovers.

### Session lifetime

A session is removed when the client sends `DELETE`, or when it has had no connected peers for longer than `server.session_grace_ms` (30 seconds by default). The grace period is what leaves the door open for an ICE restart or a redial; the sweep is what stops a client that vanished mid-call — and therefore never sent `DELETE` — from leaking a session for the life of the process.

## Realtime events

The client must create a DataChannel labeled `events` before generating the offer. The server sends:

| Type | Payload | Description |
|------|---------|-------------|
| `transcript` | `{ "type": "transcript", "text": string, "final": boolean }` | User transcript updates |
| `response` | `{ "type": "response", "text": string }` | Streamed response text |
| `state` | `{ "type": "state", "state": "listening" \| "thinking" \| "speaking" }` | Agent turn state, for UI indicators |
| `timing` | `{ "type": "timing", "stage": string, "ms": number }` | Latency timings when `pipeline.debug = true` |
| `connection` | `{ "type": "connection", "state": "reconnecting" \| "connected" }` | Transport dropped and is recovering, or has recovered |

Timing stages today: `llm_first_token`, `tts_first_byte`.

Messages the client sends on the same channel are routed into the pipeline — currently used for camera image chunks consumed by the `vision.analyze` plugin.

## Auth

Set `server.jwt_secret` to require `Authorization: Bearer <jwt>` on `/whip`. When it is set, the server also exposes `POST /token`, which issues an HS256 token valid for one hour. Set `server.api_key` to require `Authorization: Bearer <api_key>` on `/token` itself, so only your backend can mint session tokens. Both are empty by default, which disables auth.
