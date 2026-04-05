# go-mediasoup-client

A Go + Pion rewrite of the **core mediasoup-client flow**, modeled after `mediasoup-client/src`:

- `Device`
- `SendTransport`
- `RecvTransport`
- `Producer`
- `Consumer`
- `DataProducer`
- `DataConsumer`
- router RTP capabilities loading
- RTP capabilities / RTP parameters mapping
- ICE + DTLS parameter plumbing
- `produce` / `consume` callback flow
- `produceData` / `consumeData` callback flow

This is intentionally a mediasoup-client style port, not a generic Pion helper.

## What Was Mirrored From TypeScript

### `Device.load()` flow (`src/Device.ts`)

The Go `Device.Load()` follows the same sequence:

1. Validate router RTP capabilities.
2. Get native recv/send capabilities.
3. Build `getSendExtendedRtpCapabilities` closure.
4. Compute recv/send extended capabilities.
5. Derive recv/send RTP capabilities.
6. Set `canProduce(audio|video)`.
7. Load SCTP capabilities.

### ORTC mapping (`src/ortc.ts`)

Implemented Go equivalents for:

- `GetExtendedRtpCapabilities`
- `GetRecvRtpCapabilities`
- `GetSendRtpCapabilities`
- `GetSendingRtpParameters`
- `GetSendingRemoteRtpParameters`
- `ReduceCodecs`
- `GenerateProbatorRtpParameters`
- `GenerateProbatorRtpParametersFromCapabilities` (Go/Pion pre-arm helper)
- `CanSend`
- `CanReceive`
- RTP capability/parameter normalization helpers

Comments in the code call out important TS-to-Go mappings.

### Transport produce/consume flow (`src/Transport.ts`)

- `SendTransport.Produce()`:
  - validates direction and producibility
  - runs `OnConnect` once when needed
  - creates sender in Pion
  - computes mediasoup-style RTP parameters
  - invokes `OnProduce` to obtain producer id
  - returns `Producer`

- `RecvTransport.Consume()`:
  - validates direction and consumability (`CanReceive`)
  - runs `OnConnect` once when needed
  - creates recv transceiver in Pion
  - returns `Consumer`

- `SendTransport.ProduceData()`:
  - validates SCTP availability and stream reliability options
  - runs `OnConnect` once when needed
  - creates negotiated DataChannel with SCTP stream id
  - invokes `OnProduceData` to obtain data producer id
  - returns `DataProducer`

- `RecvTransport.ConsumeData()`:
  - validates SCTP stream parameters
  - runs `OnConnect` once when needed
  - creates negotiated DataChannel with the given stream id
  - returns `DataConsumer`

## API Sketch

```go
package mediasoupclient

func NewDevice(opts DeviceOptions) (*Device, error)
func (d *Device) Load(routerCaps RtpCapabilities, preferLocalCodecsOrder bool) error
func (d *Device) CreateSendTransport(opts SendTransportOptions) (*SendTransport, error)
func (d *Device) CreateRecvTransport(opts RecvTransportOptions) (*RecvTransport, error)

func (t *SendTransport) Produce(ctx context.Context, opts ProduceOptions) (*Producer, error)
func (t *RecvTransport) Consume(ctx context.Context, opts ConsumeOptions) (*Consumer, error)
func (t *RecvTransport) PrimeProbator(ctx context.Context) error
func (t *SendTransport) ProduceData(ctx context.Context, opts ProduceDataOptions) (*DataProducer, error)
func (t *RecvTransport) ConsumeData(ctx context.Context, opts ConsumeDataOptions) (*DataConsumer, error)

func NewEncodedMediaPusher(
    audioTrack *webrtc.TrackLocalStaticSample,
    videoTrack *webrtc.TrackLocalStaticSample,
) (*EncodedMediaPusher, error)
func (p *EncodedMediaPusher) PushAudioOpus(payload []byte, duration time.Duration) error
func (p *EncodedMediaPusher) PushVideoVP8(frame []byte, duration time.Duration) error
```

## Example

Go client example: `./example/main.go`  
Matching mediasoup signaling/server: `./example/mediasoup-server`

Run server:

```bash
cd example/mediasoup-server
npm install
npm start
```

Run Go client (in another terminal from repo root):

```bash
go run ./example
```

Optional:

```bash
go run ./example --signaling-url http://127.0.0.1:3000 --peer-id go-publisher --config ./example/config.toml
```

The Go example now stays alive until `Ctrl+C` (or use `--run-for 30s`) and sends periodic DataProducer heartbeat messages so interop is observable in real time.
It publishes producers based on config toggles (default: audio/data channel enabled, video disabled).
It auto-consumes all listed media/data producers by default.
It logs client-generated audio/video payload previews as `[pion-out-buffer]` and logs received RTP payload previews as `[pion-in-buffer]` when recv tracks arrive.
It primes the mediasoup RTP probator receiver on the recv transport to reduce transient Pion `ssrc(1234)` probation warnings.

### Media Source Config

`./example/config.toml` controls whether the example uses FFmpeg as source and which producers are created.

Default:

```toml
[media]
useFfmpeg = false
enableAudio = true
enableVideo = false
enableDataChannel = true
```

Set `enableAudio`, `enableVideo`, or `enableDataChannel` to `false` to skip creating that producer type.

With `useFfmpeg = false`, enabled media producers expect you to inject encoded buffers manually via:

- `EncodedMediaPusher.PushAudioOpus(payload, duration)`
- `EncodedMediaPusher.PushVideoVP8(frame, duration)`

Set `useFfmpeg = true` to keep the built-in FFmpeg test source behavior.

Push example (from your Go app):

```go
pusher, _ := mediasoupclient.NewEncodedMediaPusher(audioTrack, videoTrack)

// Opus packet from TTS/encoder (20ms example).
_ = pusher.PushAudioOpus(opusPayload, 20*time.Millisecond)

// VP8 encoded frame from your video pipeline.
_ = pusher.PushVideoVP8(vp8Frame, 33*time.Millisecond)
```

### Encoded Input Specs (`EncodedMediaPusher`)

The pusher expects already-encoded codec payloads (not raw PCM/YUV):

- `PushAudioOpus(payload, duration)`:
  - `payload`: exactly one Opus packet/frame.
  - `duration`: packet duration at 48 kHz clock (track is `audio/opus`, `48000`, `2ch`).
  - recommended packetization: 20 ms (also valid: 10/40/60 ms).
  - do not pass Ogg container pages/headers (`OpusHead`, `OpusTags`).

- `PushVideoVP8(frame, duration)`:
  - `frame`: exactly one raw VP8 encoded frame payload.
  - `duration`: frame period at 90 kHz video clock.
  - recommended frame pacing: ~33 ms for 30 fps (or match your encoder fps).
  - first frame should be a keyframe so remote decoders can start immediately.

General constraints:
- `payload/frame` must be non-empty.
- `duration` must be `> 0`.

Browser observer UI:

1. Start the server.
2. Open `http://127.0.0.1:3000` in a browser.
3. Click `Join Peer`.
4. For browser publish: click `Create Send Transport`, then `Publish AV` (prompts mic/camera permission).
5. For browser consume: click `Create Recv Transport`, then consume any listed producer/data producer.
6. Check Go client logs for `[pion-out-buffer]` / `[pion-buffer]` lines for audio/video packets.

### Voice Agent UI (Deepgram + Cartesia)

The example server also includes a separate voice-agent demo at:

```bash
http://127.0.0.1:3000/voice-agent
```

It records browser mic audio, sends it to Deepgram for STT, creates a local text reply, then synthesizes that reply with Cartesia TTS and plays the returned WAV audio in the page.

Set these before `npm start` in `example/mediasoup-server`:

```bash
export DEEPGRAM_API_KEY=...
export CARTESIA_API_KEY=...
export CARTESIA_MODEL_ID=sonic-2
export CARTESIA_VOICE_ID=794f9389-aac1-45b6-b726-9d9369183238
```

Notes:
- Browser UI imports mediasoup-client from `esm.sh`.

Example wiring mirrors mediasoup-client app signaling flow:

- load router RTP capabilities
- create send/recv transports on server
- `connect` callbacks per transport
- `produce` + server `consume`
- `producedata` + server `consumeData`

## Tests

Current tests focus on ORTC mapping and utility behavior:

```bash
go test ./...
```

`ortc_test.go` validates codec matching, payload mapping, feedback reduction, `CanReceive`, and probator parameter generation.

## Current Gaps

This implementation mirrors the essential mediasoup-client behavior, but it is not full parity yet:

- No full SDP handler parity with all browser handlers (`handlers/*` in TS).
- No batched pending consumer creation queue logic from TS.
- H264 profile-level negotiation is conservative and simpler than TS (`h264-profile-level-id` behavior).
- Pause/resume behavior is lightweight compared to browser track semantics.

These are the next areas to extend for deeper mediasoup-client parity.
