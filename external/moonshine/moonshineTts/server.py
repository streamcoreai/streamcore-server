#!/usr/bin/env python3
"""
Moonshine TTS - HTTP text-to-speech server.

POST /v1/speak    ->  raw PCM audio (16kHz, 16-bit signed LE, mono), streamed
GET  /v1/voices   ->  {"language": "...", "present": [...], "downloadable": [...]}
GET  /health      ->  {"status": "ok"}

/v1/speak mirrors Deepgram's endpoint of the same name: the text goes in the
body, encoding and sample_rate are query parameters, and the response is
headerless PCM written as it is synthesized. That makes the Go client a near
copy of the Deepgram one, and playback starts on the first clause instead of
waiting for the whole utterance.

Everything is local: moonshine-voice downloads the voice on first use and
needs no API key.
"""

import argparse
import json
import logging
import threading

import numpy as np
from fastapi import FastAPI, HTTPException, Query, Request
from fastapi.responses import StreamingResponse
import uvicorn

from moonshine_voice import (
    MoonshineError,
    TextToSpeech,
    list_tts_voices,
)

logger = logging.getLogger("moonshine-tts")

TARGET_SAMPLE_RATE = 16000

# Defaults, overridden by --language / --voice.
DEFAULT_LANGUAGE = "en_us"
DEFAULT_VOICE = "kokoro_af_heart"

# One engine per (language, voice). A synthesizer speaks one thing at a time —
# the native layer rejects a second generation while one is in flight — so the
# lock is held for the whole response, not just while starting it.
_engines = {}
_engine_lock = threading.Lock()
_synthesis_lock = threading.Lock()


def get_engine(language: str, voice: str) -> TextToSpeech:
    """Return the loaded engine for a voice, loading (and downloading) it once."""
    key = (language, voice)
    with _engine_lock:
        engine = _engines.get(key)
        if engine is not None:
            return engine

        logger.info(f"Loading voice {voice!r} for language {language!r}...")
        engine = TextToSpeech().language(language).voice(voice)
        engine.load()
        _engines[key] = engine
        logger.info(f"Voice {voice!r} ready")
        return engine


class LinearResampler:
    """Resamples a stream of float chunks with linear interpolation.

    Resampling each chunk on its own leaves a discontinuity at every join,
    which is audible as a click once a sentence is cut into a dozen of them.
    Carrying the last source sample and the fractional read position across
    calls makes the joins land where they would in one continuous pass.
    """

    def __init__(self, src_rate: int, dst_rate: int):
        self.ratio = src_rate / dst_rate
        self.next_t = 0.0  # next output position, in source samples
        self.base = 0.0  # global index of tail[0]
        self.tail = np.empty(0, dtype=np.float32)

    def process(self, samples: np.ndarray) -> np.ndarray:
        if self.ratio == 1.0:
            return samples

        buf = np.concatenate([self.tail, samples])
        highest = self.base + len(buf) - 1  # last position we can interpolate

        out = np.empty(0, dtype=np.float32)
        if self.next_t <= highest:
            count = int(np.floor((highest - self.next_t) / self.ratio)) + 1
            positions = self.next_t + self.ratio * np.arange(count)
            out = np.interp(positions - self.base, np.arange(len(buf)), buf)
            self.next_t += self.ratio * count

        self.tail = buf[-1:]
        self.base = self.base + len(buf) - 1
        return out


def to_pcm16(samples: np.ndarray) -> bytes:
    """Float samples in -1..1 to little-endian signed 16-bit PCM."""
    if len(samples) == 0:
        return b""
    return np.clip(samples * 32767.0, -32768, 32767).astype(np.int16).tobytes()


def synthesize_stream(engine: TextToSpeech, text: str, sample_rate: int):
    """Yield PCM as the model produces it, one chunk per piece of the reply."""
    with _synthesis_lock:
        resampler = None
        try:
            for chunk in engine.stream(text):
                samples = np.asarray(chunk.samples, dtype=np.float32)
                if resampler is None:
                    resampler = LinearResampler(chunk.sample_rate, sample_rate)
                pcm = to_pcm16(resampler.process(samples))
                if pcm:
                    yield pcm
        finally:
            # A caller that hangs up mid-sentence (barge-in does exactly that)
            # closes this generator early, leaving a generation in flight that
            # would reject the next request.
            if engine.is_streaming:
                engine.cancel_stream()


app = FastAPI(title="Moonshine TTS")


@app.post("/v1/speak")
async def speak(
    request: Request,
    voice: str = Query(default=None),
    language: str = Query(default=None),
    encoding: str = Query(default="linear16"),
    sample_rate: int = Query(default=TARGET_SAMPLE_RATE),
    container: str = Query(default="none"),
):
    if encoding != "linear16":
        raise HTTPException(400, f"unsupported encoding {encoding!r}; only linear16")
    if container != "none":
        raise HTTPException(400, f"unsupported container {container!r}; only none")
    if sample_rate <= 0:
        raise HTTPException(400, "sample_rate must be positive")

    text = await read_text(request)
    if not text.strip():
        raise HTTPException(400, "text must not be empty")

    language = language or DEFAULT_LANGUAGE
    voice = voice or DEFAULT_VOICE
    try:
        engine = get_engine(language, voice)
    except MoonshineError as e:
        raise HTTPException(400, str(e))

    logger.info(f"Synthesizing {len(text)} chars with {voice!r}")
    return StreamingResponse(
        synthesize_stream(engine, text, sample_rate),
        media_type="audio/pcm",
    )


async def read_text(request: Request) -> str:
    """Accept either a text/plain body or Deepgram's {"text": "..."} JSON."""
    body = await request.body()
    if request.headers.get("content-type", "").startswith("application/json"):
        try:
            return json.loads(body).get("text", "")
        except (json.JSONDecodeError, AttributeError):
            raise HTTPException(400, "body is not valid JSON")
    return body.decode("utf-8", errors="replace")


@app.get("/v1/voices")
async def voices(language: str = Query(default=None)):
    language = language or DEFAULT_LANGUAGE
    try:
        by_availability = list_tts_voices(language)
    except MoonshineError as e:
        raise HTTPException(400, str(e))
    return {
        "language": language,
        "present": by_availability.get("present", []),
        "downloadable": by_availability.get("downloadable", []),
    }


@app.get("/health")
async def health():
    return {"status": "ok"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Moonshine TTS HTTP Server")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=8310, help="Bind port")
    parser.add_argument(
        "--language", default=DEFAULT_LANGUAGE, help="Default synthesis language"
    )
    parser.add_argument(
        "--voice",
        default=DEFAULT_VOICE,
        help="Default voice id. The prefix picks the vocoder: kokoro_, piper_, "
        "or zipvoice_",
    )
    parser.add_argument(
        "--preload",
        action="store_true",
        help="Download and load the default voice at startup rather than on the "
        "first request",
    )
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper()),
        format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    )

    DEFAULT_LANGUAGE = args.language
    DEFAULT_VOICE = args.voice

    if args.preload:
        get_engine(DEFAULT_LANGUAGE, DEFAULT_VOICE)

    uvicorn.run(app, host=args.host, port=args.port, log_level=args.log_level.lower())
