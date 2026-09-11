**English** | [简体中文](./protocol.zh-CN.md)

# Protocol reference

## WHIP signaling

Signaling follows [RFC 9725](https://www.rfc-editor.org/rfc/rfc9725.html).

| Step | Method | Path | Body | Response |
|------|--------|------|------|----------|
| 1 | `POST` | `/whip` | SDP offer (`application/sdp`) | `201 Created` with SDP answer, `Location: /whip/{sessionId}`, `ETag`, and `Accept-Patch` |
| 2 | `PATCH` | `/whip/{sessionId}` | ICE restart fragment (`application/trickle-ice-sdpfrag`) | `200 OK` with the server's fragment and a new `ETag` |
| 3 | `DELETE` | `/whip/{sessionId}` | none | `200 OK` |
| — | `POST` | `/whip?resume={token}` | SDP offer (`application/sdp`) | `201 Created`, reattached to the token's session — a StreamCore extension, see [Session resume](#session-resume) |
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

### The recovery ladder

The two mechanisms above are not alternatives — they are ordered, and every SDK
except Python runs both in sequence:

| When | Mechanism | What survives |
|------|-----------|---------------|
| Connection is `disconnected` | **ICE restart** (`PATCH`) | Everything. Same `PeerConnection`, same DTLS, same tracks — nothing above the transport notices. |
| Connection has `failed` | **Session resume** (`POST ?resume=`) | The conversation. The transport is rebuilt from scratch; history, rolling summary and agent memory carry over. |
| Resume token expired or session reaped | Nothing | A working call with a blank conversation. The client is told via `X-Resume-Status: expired`. |

The two phases share one deadline. `disconnected` escalates to `failed` after
roughly 25 seconds, and the server then holds the abandoned conversation for
`server.session_grace_ms` (30 seconds by default) before reaping it. Time spent
on restarts is time not available for redials, so an SDK that is generous with
`reconnectAttempts` has less budget left for `resumeAttempts`.

Why both, rather than only resume? Because an ICE restart is invisible: no new
DTLS handshake, no track churn, no gap beyond the ICE recheck. A resume redial
costs a full renegotiation and a moment of silence. Restart is strictly better
when it is available, and resume is the only thing that works when it is not.

### Session resume

**A StreamCore extension, not part of RFC 9725.**

ICE restart recovers a transport that is broken. It cannot recover one that is *gone*: past roughly 25 seconds the connection is `failed`, the server has closed the peer, and there is nothing left to restart. A client that was backgrounded, suspended, or offline for a minute lands here, as does any client whose WebRTC stack cannot perform an ICE restart at all — the Python SDK among them.

Resume covers that case by letting a fresh `POST` reattach to the conversation the dead connection was having. The transport is new; the session, the LLM client with its message history, the transcript log, and the rolling summary are not, and the greeting does not replay.

Every `POST` response carries a token and a status:

```http
HTTP/1.1 201 Created
Location: /whip/{sessionId}
X-Resume-Status: new
X-Resume-Token: qcH8tnK-zT8...
```

To resume, redial with the token as a query parameter:

```http
POST /whip?resume=qcH8tnK-zT8... HTTP/1.1
Content-Type: application/sdp
```

| `X-Resume-Status` | Meaning |
|-------------------|---------|
| `new` | No token was sent. A fresh conversation. |
| `resumed` | Reattached. `Location` is the original session URL and the agent remembers the call. |
| `expired` | The token was unknown, already spent, or its session was reaped. **A working call, but a blank conversation** — the agent has no memory of what came before. |

A redial with a stale token is never rejected outright, because failing the call outright is worse than losing the history. The status header is how the client tells the difference; silently starting over is the exact amnesia the token exists to prevent.

Two properties worth relying on:

- **Tokens are single-use.** Every response issues a fresh one and invalidates its predecessor, so a token captured from a log or a proxy is worthless the moment the legitimate client redials.
- **The token is not the session ID.** The ID appears in the resource URL and in logs; a credential that grants access to a live conversation should be neither, so it is 32 bytes from `crypto/rand`.

The window is `server.session_grace_ms` (30 seconds by default) measured from when the last peer left. Raise it to give flaky clients longer, at the cost of holding abandoned conversations in memory.

Resume is not offered for realtime (speech-to-speech) sessions: their history lives inside the provider and a new provider connection cannot inherit it, so no token is issued rather than promising continuity the server cannot deliver.

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
| `display.card` | `{ "type": "display.card", "version": 1, ... }` | Optional persistent-display projection; sent only when `[display] enabled = true` |

Timing stages today: `llm_first_token`, `tts_first_byte`.

Messages the client sends on the same channel are routed into the pipeline — currently used for camera image chunks consumed by the `vision.analyze` plugin.

### Display cards

`display.card` is a small, versioned semantic payload for slow persistent displays such as e-ink. It is additive and disabled by default. Unknown clients should ignore it, just as the NOTE4C ignores realtime `transcript`, `response`, and `state` events for persistent rendering.

```json
{
  "type": "display.card",
  "version": 1,
  "session_id": "9d2f...",
  "turn_id": "turn_123",
  "turn_seq": 123,
  "card": {
    "layout": "hero",
    "title": "Auckland · Tomorrow",
    "primary": "17°C",
    "secondary": "Rain after lunch",
    "detail": "Mostly cloudy · light winds"
  }
}
```

**On the wire it arrives wrapped.** A card is a plugin emission, and every
topic-addressed emission reaches the client inside a data packet, base64 and
all:

```json
{ "type": "data", "topic": "display.card", "payload": "<base64 of the object above>" }
```

So a client reads cards from its **topic-addressed data callback**, not from
the raw event stream — the raw stream carries the envelope, whose `type` is
`data`. A client that parses the envelope as a card sees the wrong type and
drops every card it is sent. The ESP32 SDK unwraps this for you and hands the
decoded bytes to `on_data(topic, payload)`; `on_raw_event` sees the envelope.
Unlike `transcript`, `response` and `state`, which are sent as bare events,
nothing sends a bare `display.card` today — accept one if you like, but take
the wrapped form or you will receive nothing.

`turn_seq` is a positive integer that increases within `session_id`; `turn_id` is its human-readable counterpart. A device with a slow display should keep only the newest valid card, not a FIFO. Reject a card whose `turn_seq` is not newer than the latest accepted card from the same session. A changed `session_id` starts a new sequence.

The server validates and truncates all fields before sending:

| Field | Layouts | Limit |
|---|---|---|
| `title` | all | 32 characters |
| `primary` | `hero`, `status` | 24 characters |
| `secondary` | `hero`, `status` | 48 characters |
| `detail` | `hero` | 80 characters |
| `body` | `text` | 180 characters |
| `items` | `list` | 4 items |
| each `items[]` | `list` | 40 characters |
| `columns` | `split` | 2 columns |
| `columns[].heading` | `split` | 17 characters |
| `columns[].lines` | `split` | 7 lines |
| each `columns[].lines[]` | `split` | 17 characters |
| `weather.forecast` | `weather` | 3 days |
| `weather.note` | `weather` | 34 characters |
| each temperature | `weather` | 4 characters |
| each `forecast[].label` | `weather` | 4 characters |
| `agents` | `usage` | 3 agents |
| `agents[].name` | `usage` | 18 characters |
| `agents[].note` | `usage` | 14 characters |
| `agents[].gauges` | `usage` | 3 gauges |
| `gauges[].label` | `usage` | 3 characters |
| `gauges[].reset` | `usage` | 8 characters |

Layout semantics:

- **hero** — one dominant fact (`title`, required `primary`, optional `secondary`/`detail`).
- **text** — a compact explanation (`title`, required `body`).
- **list** — at most four short entries (`title`, required non-empty `items`).
- **status** — a completed action or confirmation (`title`, required `primary`, optional `secondary`).
- **weather** — a forecast (`title`, required `primary` and `weather`). See below.
- **usage** — one meter per subject (`title`, required non-empty `agents`). See below.
- **split** — two things side by side (`title`, required non-empty `columns`). Each column carries a `heading` and short `lines`; a blank line is a spacer the client spends a row on. For comparing one thing against another, which no single-column layout can express.

```json
{
  "layout": "split",
  "title": "AI Usage",
  "columns": [
    { "heading": "Claude 2m", "lines": ["5H 62% used", "======----", "38% left 2h 10m"] },
    { "heading": "Codex 5m", "lines": ["5H 16% used", "==--------", "84% left 4h"] }
  ]
}
```

`split` is for plugins that emit their own cards; the display projector never produces one, since a projected turn has one subject rather than two. A client that predates the layout rejects the card as unknown and keeps whatever it was showing.

#### weather

Two subjects come up often enough on a persistent display to have earned a structured layout of their own, and the weather is one. The card still carries no pixels: it names a condition, and the client owns every icon it draws for it.

```json
{
  "layout": "weather",
  "title": "Auckland",
  "primary": "17",
  "secondary": "Partly cloudy",
  "detail": "Saturday 18 May",
  "weather": {
    "icon": "partly",
    "unit": "C",
    "high": "19",
    "low": "11",
    "note": "Take an umbrella Monday",
    "forecast": [
      { "label": "SUN", "icon": "rain", "high": "18", "low": "10" },
      { "label": "MON", "icon": "sun", "high": "21", "low": "12" }
    ]
  }
}
```

`title` is the place, `primary` the current temperature as bare digits, `secondary` the condition in words, and `detail` the day it is for. Temperatures carry no degree sign or unit — `unit` says `C` or `F` once, and the client draws the rest, because a panel that maps non-ASCII to `?` cannot render a `°` that arrives inside a string.

`icon` is one of `sun`, `moon`, `partly`, `cloud`, `rain`, `storm`, `snow`, `fog`, `wind`. **A client must not reject a card over an icon name it does not know** — it draws a neutral one and keeps the temperature, which is still the answer. `note` is one line of advice across the foot of the card, and is optional; `forecast` is optional and holds up to three days.

#### usage

The other: how much of something metered is gone. One panel per subject, one gauge per window.

```json
{
  "layout": "usage",
  "title": "AI Usage",
  "agents": [
    {
      "name": "Claude Code",
      "note": "as of 06:11",
      "gauges": [
        { "label": "5H", "percent": 62, "reset": "@14:10" },
        { "label": "7D", "percent": 17, "reset": "@Sep 7" }
      ]
    },
    { "name": "Codex", "note": "no recent runs", "gauges": [] }
  ]
}
```

`percent` is the percentage **used**: an integer from 0 to 100, or `null` where the source did not say — which a client must draw apart from zero, since an unread limit and an untouched one are not the same fact.

Used, because that is the raw fact the sources report. A client is free to draw what is *left* instead — the NOTE4C does, since "how much have I got" is the question you ask a wall panel — but whichever it picks, **it has to say which on the face of it**. The tools themselves disagree: Claude Code's `/usage` says "26% used", the Codex CLI says "100% remaining". A bare percentage is a number the reader has to guess the polarity of, and half of them will guess wrong. A value outside the range is clamped rather than rejected. `reset` is already formatted by the server, which is the only side that knows its own local time, and `gauges` may be empty: an agent with nothing to report keeps its panel and explains itself in `note`.

Both layouts are for plugins that emit their own cards. The display projector produces neither, because it works from a finished turn's text and would be inventing the structure.

The payload contains semantics only. StreamCore does not send coordinates, fonts, colours, framebuffers, PNGs, or device-specific rendering instructions. The client chooses typography, wrapping, colour use, and when a refresh is affordable. A recommended device policy is: store latest-wins, wait until assistant playback and the next-user check have gone idle, debounce for 1–3 seconds, then spend one refresh.

## Auth

Set `server.jwt_secret` to require `Authorization: Bearer <jwt>` on `/whip`. When it is set, the server also exposes `POST /token`, which issues an HS256 token valid for one hour. Set `server.api_key` to require `Authorization: Bearer <api_key>` on `/token` itself, so only your backend can mint session tokens. Both are empty by default, which disables auth.

### Caller identity

`POST /token` accepts an optional body naming who the token is for:

```json
{ "resource_id": "user_8891" }
```

The value is signed into the token as its `sub` claim, and `/whip` forwards it to an external agent as `resource_id` (see [Bring your own agent](./bring-your-own-agent.md)). Where `session_id` scopes one conversation, this scopes the person across all of them — it is what lets an agent recognise a caller who hung up yesterday and rang back today.

Mint it here rather than sending it from the client: `/token` is called by your backend, which holds the API key and already knows which user is signed in, while `/whip` is called by the browser. A page cannot forge a claim it never signs.

Server-side clients that dial `/whip` directly without a token endpoint may instead send:

```http
X-StreamCore-Resource-Id: +14155550123
```

The header is consulted **only** when the request carries no signed claim, so it can never override one. It is also absent from the CORS `Access-Control-Allow-Headers` list, which means browsers cannot send it at all — it exists for trusted server-side callers such as [sip-server](../../sip-server/README.md), which sets it to the number a call came from.

Identity is optional throughout. A deployment that asserts none simply omits `resource_id` from agent requests, and the agent falls back to session-scoped memory.
