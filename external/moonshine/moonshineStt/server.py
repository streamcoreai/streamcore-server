#!/usr/bin/env python3
"""
Moonshine STT - live WebSocket speech-to-text server.

Accepts raw PCM audio (16kHz, 16-bit, mono) over WebSocket and answers with
Deepgram's live-transcription frames, so the Go server can route Moonshine
through the same accumulate-and-dedupe path it already runs for Deepgram.

Everything is local: moonshine-voice downloads the model on first run and
needs no API key.

Protocol:
  Client -> Server: binary frames (raw PCM, linear16, 16kHz, mono)
                    text frames  {"type": "KeepAlive" | "Finalize" | "CloseStream"}
  Server -> Client: text frames  Results / SpeechStarted / UtteranceEnd / Metadata

Moonshine reports no per-transcript confidence, so the Results frames leave
the field out. Deepgram's Go types decode a missing confidence as 0, which
the pipeline reads as "unknown" rather than "low".
"""

import argparse
import asyncio
import json
import logging
import time
import uuid
from concurrent.futures import ThreadPoolExecutor

import numpy as np
import websockets

from moonshine_voice import (
    Transcriber,
    TranscriptEventListener,
    get_model_for_language,
    model_arch_to_string,
    string_to_model_arch,
)

logger = logging.getLogger("moonshine-stt")

SAMPLE_RATE = 16000
SAMPLE_WIDTH = 2  # 16-bit

# Deepgram reports single-channel audio as channel 0 of 1.
CHANNEL_INDEX = [0, 1]

# Audio is handed to the model in 100ms batches. Inbound frames are 20ms of
# RTP, and a native call per frame pays the fixed per-pass cost five times
# over for the same audio.
FLUSH_SAMPLES = SAMPLE_RATE // 10

# Seconds of audio between transcription passes; --update-interval overrides it.
UPDATE_INTERVAL = 0.5

# One shared transcriber holds the model weights, and one worker thread runs
# every native call against it. Serialising on the executor rather than a lock
# is what keeps concurrent sessions from reentering the C API — each session
# still gets its own stream, so their transcripts stay independent.
_transcriber = None
_model_arch = None
_executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="moonshine-stt")


def load_model(language: str, model_arch):
    """Download (first run) and load the transcription model for a language."""
    global _transcriber, _model_arch

    logger.info(f"Loading Moonshine model for language {language!r}...")
    model_path, resolved_arch = get_model_for_language(language, model_arch)
    _transcriber = Transcriber(model_path=model_path, model_arch=resolved_arch)
    _model_arch = resolved_arch
    logger.info(f"Model loaded: {model_arch_to_string(resolved_arch)} at {model_path}")


# ---------------------------------------------------------------------------
# Deepgram wire frames
# ---------------------------------------------------------------------------


def results_frame(line, text: str, is_final: bool) -> dict:
    """Build a Deepgram Results frame for one transcript line."""
    return {
        "type": "Results",
        "channel_index": CHANNEL_INDEX,
        "start": round(float(line.start_time), 4),
        "duration": round(float(line.duration), 4),
        "is_final": is_final,
        # Deepgram separates the two because it freezes words mid-sentence and
        # only later decides the utterance ended. Moonshine completes a line
        # exactly once, at the end of the utterance, so every final it reports
        # is a speech_final too.
        "speech_final": is_final,
        "channel": {"alternatives": [{"transcript": text}]},
    }


def speech_started_frame(timestamp: float) -> dict:
    return {
        "type": "SpeechStarted",
        "channel": CHANNEL_INDEX,
        "timestamp": round(float(timestamp), 4),
    }


def utterance_end_frame(last_word_end: float) -> dict:
    return {
        "type": "UtteranceEnd",
        "channel": CHANNEL_INDEX,
        "last_word_end": round(float(last_word_end), 4),
    }


def error_frame(err: Exception) -> dict:
    return {
        "type": "Error",
        "description": str(err),
        "message": err.__class__.__name__,
    }


class DeepgramFrameListener(TranscriptEventListener):
    """Turns Moonshine transcript events into Deepgram wire frames.

    The callbacks fire on the worker thread, inside add_audio. Frames are
    queued here and sent once that call returns, which keeps websocket sends
    on the event loop and keeps them in the order the model produced them.
    """

    def __init__(self):
        self.pending = []

    def on_line_started(self, event):
        self.pending.append(speech_started_frame(event.line.start_time))

    def on_line_text_changed(self, event):
        # A line that just completed fires this immediately before
        # on_line_completed carrying the same words. The final below covers
        # them, so an interim here would only send the sentence twice.
        if event.line.is_complete:
            return
        text = event.line.text.strip()
        if text:
            self.pending.append(results_frame(event.line, text, is_final=False))

    def on_line_completed(self, event):
        line = event.line
        text = line.text.strip()
        if text:
            self.pending.append(results_frame(line, text, is_final=True))
        # Deepgram sends UtteranceEnd alongside speech_final, and the Go
        # callback uses it to flush anything a missing speech_final left
        # buffered. Sending both keeps that fallback path exercised.
        self.pending.append(utterance_end_frame(line.start_time + line.duration))

    def on_error(self, event):
        logger.error(f"Transcriber error: {event.error}")
        self.pending.append(error_frame(event.error))

    def drain(self):
        frames, self.pending = self.pending, []
        return frames


# ---------------------------------------------------------------------------
# Session
# ---------------------------------------------------------------------------


class STTSession:
    """One WebSocket client: its own transcription stream and audio buffer."""

    def __init__(self, ws, loop, update_interval: float):
        self.ws = ws
        self.loop = loop
        self.update_interval = update_interval
        self.listener = DeepgramFrameListener()
        self.stream = None
        self.request_id = str(uuid.uuid4())
        self.started_at = time.monotonic()
        self._pending = np.empty(0, dtype=np.int16)

    async def _run(self, fn, *args):
        return await self.loop.run_in_executor(_executor, fn, *args)

    async def open(self):
        self.stream = await self._run(self._open_stream)

    def _open_stream(self):
        stream = _transcriber.create_stream(update_interval=self.update_interval)
        stream.add_listener(self.listener)
        stream.start()
        return stream

    async def handle_audio(self, data: bytes):
        samples = np.frombuffer(data, dtype=np.int16)
        self._pending = np.concatenate([self._pending, samples])
        if len(self._pending) >= FLUSH_SAMPLES:
            await self._feed_pending()

    async def _feed_pending(self):
        if len(self._pending) == 0:
            return
        chunk, self._pending = self._pending, np.empty(0, dtype=np.int16)
        audio = (chunk.astype(np.float32) / 32768.0).tolist()
        await self._run(self.stream.add_audio, audio, SAMPLE_RATE)
        await self._send_frames()

    async def finalize(self):
        """Transcribe everything buffered so far, the way Deepgram's Finalize does."""
        if len(self._pending):
            await self._feed_pending()
            return
        await self._run(self.stream.update_transcription)
        await self._send_frames()

    async def _send_frames(self):
        for frame in self.listener.drain():
            if frame["type"] == "Results":
                text = frame["channel"]["alternatives"][0]["transcript"]
                kind = "FINAL" if frame["is_final"] else "PARTIAL"
                logger.info(f"{kind}: {text}")
            await self.ws.send(json.dumps(frame))

    async def close(self):
        if self.stream is None:
            return
        try:
            await self._feed_pending()
            # stop() transcribes what is left in the stream, so the tail of an
            # utterance still reaches the caller as a final rather than being
            # dropped with the connection.
            await self._run(self.stream.stop)
            await self._send_frames()
            await self.ws.send(json.dumps(self._metadata_frame()))
        except websockets.exceptions.ConnectionClosed:
            pass
        finally:
            await self._run(self.stream.close)
            self.stream = None

    def _metadata_frame(self) -> dict:
        return {
            "type": "Metadata",
            "request_id": self.request_id,
            "channels": 1,
            "duration": round(time.monotonic() - self.started_at, 4),
            "models": [model_arch_to_string(_model_arch)],
        }


async def handle_connection(ws):
    """Handle one WebSocket client session."""
    logger.info("New STT session connected")
    session = STTSession(ws, asyncio.get_running_loop(), UPDATE_INTERVAL)
    await session.open()

    try:
        async for message in ws:
            if isinstance(message, bytes):
                await session.handle_audio(message)
                continue

            kind = _control_type(message)
            if kind == "Finalize":
                await session.finalize()
            elif kind == "CloseStream":
                break
            elif kind != "KeepAlive":
                logger.debug(f"Ignoring text message: {message[:120]}")
    except websockets.exceptions.ConnectionClosed:
        logger.info("STT session disconnected")
    finally:
        await session.close()


def _control_type(message: str) -> str:
    try:
        return json.loads(message).get("type", "")
    except (json.JSONDecodeError, AttributeError):
        return ""


async def main(host: str, port: int):
    logger.info(f"Starting Moonshine STT WebSocket server on ws://{host}:{port}")
    async with websockets.serve(handle_connection, host, port, max_size=2**20):
        await asyncio.Future()  # run forever


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Moonshine STT WebSocket Server")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=8210, help="Bind port")
    parser.add_argument(
        "--language",
        default="en",
        help="Two-letter language code (default: en). Anything but 'en' loads a "
        "model under the non-commercial Moonshine Community License",
    )
    parser.add_argument(
        "--model-arch",
        default=None,
        help="tiny, base, tiny-streaming, base-streaming, small-streaming, or "
        "medium-streaming. Omit to use the catalog default for the language",
    )
    parser.add_argument(
        "--update-interval",
        type=float,
        default=0.5,
        help="Seconds of audio between transcription passes. This is a floor: "
        "a machine that cannot keep up lets passes grow rather than falling "
        "further behind on every one",
    )
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper()),
        format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    )

    UPDATE_INTERVAL = args.update_interval

    arch = string_to_model_arch(args.model_arch) if args.model_arch else None
    load_model(args.language, arch)

    asyncio.run(main(args.host, args.port))
