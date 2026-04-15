#!/usr/bin/env python3
"""
VibeVoice ASR - Live WebSocket streaming speech-to-text server.

Accepts raw PCM audio (16kHz, 16-bit, mono) over WebSocket,
buffers it, and returns transcription results as JSON.

Uses Silero VAD to gate transcription to real speech only.
Uses mlx-audio on Apple Silicon, falls back to PyTorch on other platforms.

Protocol:
  Client → Server: binary frames (raw PCM, linear16, 16kHz, mono)
  Server → Client: JSON text frames {"text": "...", "is_final": true/false}
"""

import asyncio
import json
import argparse
import logging
import tempfile
import os
import re
import wave
import time
import platform

import numpy as np
import onnxruntime as ort
import websockets

logger = logging.getLogger("vibevoice-asr")

SAMPLE_RATE = 16000
SAMPLE_WIDTH = 2  # 16-bit
CHANNELS = 1

# Silero VAD processes audio in fixed-size chunks.
# For 16 kHz, valid sizes are 512 (32ms), 1024 (64ms), or 1536 (96ms).
VAD_CHUNK_SAMPLES = 512


def is_apple_silicon():
    return platform.system() == "Darwin" and platform.machine() == "arm64"


# ---------------------------------------------------------------------------
# Silero VAD (ONNX Runtime — no torch required)
# ---------------------------------------------------------------------------
_vad_session = None
_vad_state = np.zeros(
    (2, 1, 128), dtype=np.float32
)  # Silero VAD state tensor (h and c combined)
_vad_context = np.zeros(64, dtype=np.float32)  # 64-sample context window for VAD

VAD_CONTEXT_SIZE = 64


def load_vad():
    """Load the Silero VAD model (ONNX-based, lightweight)."""
    global _vad_session
    model_path = _ensure_vad_model()
    logger.info(f"Loading Silero VAD from {model_path}...")
    _vad_session = ort.InferenceSession(
        model_path,
        providers=["CoreMLExecutionProvider", "CPUExecutionProvider"],
    )
    logger.info("Silero VAD loaded")


def _ensure_vad_model() -> str:
    """Download Silero VAD ONNX model if not present."""
    import requests

    cache_dir = os.path.expanduser("~/.cache/silero_vad")
    os.makedirs(cache_dir, exist_ok=True)
    model_path = os.path.join(cache_dir, "silero_vad.onnx")

    if not os.path.exists(model_path) or os.path.getsize(model_path) < 100000:
        url = "https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx"
        logger.info(f"Downloading Silero VAD model to {model_path}...")
        try:
            os.remove(model_path)
        except FileNotFoundError:
            pass
        response = requests.get(url, allow_redirects=True, timeout=60)
        response.raise_for_status()
        with open(model_path, "wb") as f:
            f.write(response.content)
        logger.info(f"Download complete ({len(response.content)} bytes)")
    return model_path


def vad_speech_prob(pcm_int16: np.ndarray) -> float:
    """Return speech probability [0,1] for a chunk of PCM samples.

    The input must be exactly VAD_CHUNK_SAMPLES long (512 samples @ 16 kHz).
    """
    global _vad_state, _vad_context
    audio_float = pcm_int16.astype(np.float32) / 32768.0
    # Prepend context (last 64 samples from previous call) as the model expects.
    input_with_context = np.concatenate([_vad_context, audio_float])
    input_tensor = input_with_context.reshape(1, -1)
    sr_tensor = np.array(SAMPLE_RATE, dtype=np.int64)  # 0-d array (scalar tensor)

    outputs = _vad_session.run(
        None,
        {
            "input": input_tensor,
            "sr": sr_tensor,
            "state": _vad_state,
        },
    )
    prob = float(outputs[0].item())
    _vad_state = outputs[1]  # updated state
    _vad_context = input_with_context[-VAD_CONTEXT_SIZE:]  # carry forward context
    return prob


def reset_vad_states():
    """Reset VAD RNN state between utterances."""
    global _vad_state, _vad_context
    _vad_state = np.zeros((2, 1, 128), dtype=np.float32)
    _vad_context = np.zeros(VAD_CONTEXT_SIZE, dtype=np.float32)


# ---------------------------------------------------------------------------
# ASR model
# ---------------------------------------------------------------------------
_model = None
_backend = None
_inference_lock = asyncio.Lock()  # MLX is NOT thread-safe; serialize all model access

# Regex to filter out noise tags the ASR model emits for non-speech audio.
_NOISE_TAG_RE = re.compile(
    r"^\s*(\[.*?\]\s*)*$"  # matches strings made entirely of [Tag] tokens
)


def load_model(model_name):
    """Load ASR model. Auto-selects MLX on Apple Silicon, PyTorch otherwise."""
    global _model, _backend

    if is_apple_silicon():
        try:
            from mlx_audio.stt.utils import load

            logger.info("Using MLX backend")
            logger.info(f"Loading model: {model_name}")
            _model = load(model_name)
            _backend = "mlx"
            logger.info("Model loaded successfully")
            return
        except ImportError:
            logger.warning("mlx-audio not installed, falling back to PyTorch")

    # PyTorch fallback
    try:
        import torch
        from transformers import AutoModelForCausalLM, AutoProcessor

        logger.info("Using PyTorch backend")
        logger.info(f"Loading model: {model_name}")

        _model = {
            "processor": AutoProcessor.from_pretrained(
                model_name, trust_remote_code=True
            ),
            "model": AutoModelForCausalLM.from_pretrained(
                model_name,
                trust_remote_code=True,
                torch_dtype=(
                    torch.float16 if torch.cuda.is_available() else torch.float32
                ),
                device_map="auto" if torch.cuda.is_available() else None,
            ),
        }
        _backend = "pytorch"
        logger.info("Model loaded successfully")
    except Exception as e:
        raise RuntimeError(
            f"Failed to load model: {e}\n"
            "Install mlx-audio (Apple Silicon): pip install mlx-audio\n"
            "Install PyTorch: pip install torch transformers"
        )


def _extract_text(raw_text: str) -> str:
    """Extract plain text from VibeVoice-ASR structured JSON output."""
    try:
        segments = json.loads(raw_text)
        if isinstance(segments, list):
            return " ".join(
                seg.get("Content", "") for seg in segments if seg.get("Content")
            ).strip()
    except (json.JSONDecodeError, TypeError):
        pass
    return raw_text.strip()


def _is_noise_only(text: str) -> bool:
    """Return True if text is empty or only noise tags like [Silence]."""
    return not text or bool(_NOISE_TAG_RE.match(text))


def transcribe_audio(wav_path):
    """Transcribe a WAV file and return cleaned text, or '' if noise-only."""
    if _backend == "mlx":
        result = _model.generate(audio=wav_path, max_tokens=8192, temperature=0.0)
        text = _extract_text(result.text)
        return "" if _is_noise_only(text) else text

    elif _backend == "pytorch":
        import torch
        import librosa

        audio, sr = librosa.load(wav_path, sr=16000)
        processor = _model["processor"]
        model = _model["model"]

        inputs = processor(
            audios=audio,
            sampling_rate=sr,
            return_tensors="pt",
            trust_remote_code=True,
        )
        if torch.cuda.is_available():
            inputs = {k: v.cuda() for k, v in inputs.items()}

        with torch.no_grad():
            output_ids = model.generate(**inputs, max_new_tokens=8192)

        raw = processor.batch_decode(output_ids, skip_special_tokens=True)[0]
        text = _extract_text(raw)
        return "" if _is_noise_only(text) else text

    return ""


def pcm_to_wav(pcm_bytes, wav_path):
    """Write raw PCM bytes to a WAV file."""
    with wave.open(wav_path, "wb") as wf:
        wf.setnchannels(CHANNELS)
        wf.setsampwidth(SAMPLE_WIDTH)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(pcm_bytes)


# ---------------------------------------------------------------------------
# Session
# ---------------------------------------------------------------------------


class ASRSession:
    """Manages audio buffering and transcription for one WebSocket connection."""

    def __init__(self, ws, silence_timeout=0.8, vad_threshold=0.5):
        self.ws = ws
        self.audio_buffer = bytearray()
        self.silence_timeout = silence_timeout
        self.vad_threshold = vad_threshold

        # Pending PCM samples that haven't formed a full VAD chunk yet.
        self._pending_samples = np.empty(0, dtype=np.int16)

        self.last_speech_time = 0.0
        self.speech_active = False
        self.lock = asyncio.Lock()
        self.running = True

    async def handle_audio(self, data: bytes):
        """Run Silero VAD on incoming audio; buffer only speech frames."""
        samples = np.frombuffer(data, dtype=np.int16)

        async with self.lock:
            # Accumulate samples until we have full VAD chunks.
            self._pending_samples = np.concatenate([self._pending_samples, samples])

            while len(self._pending_samples) >= VAD_CHUNK_SAMPLES:
                chunk = self._pending_samples[:VAD_CHUNK_SAMPLES]
                self._pending_samples = self._pending_samples[VAD_CHUNK_SAMPLES:]

                prob = vad_speech_prob(chunk)

                if prob >= self.vad_threshold:
                    # Speech detected — buffer it.
                    self.speech_active = True
                    self.last_speech_time = time.monotonic()
                    self.audio_buffer.extend(chunk.tobytes())
                elif self.speech_active:
                    # Still in a speech segment — include trailing audio
                    # so the ASR model gets the tail of the utterance.
                    self.audio_buffer.extend(chunk.tobytes())

    async def run_transcription_loop(self):
        """Background loop: detects silence boundaries and triggers transcription."""
        while self.running:
            await asyncio.sleep(0.1)

            async with self.lock:
                buf_len = len(self.audio_buffer)
                speech = self.speech_active
                last_speech = self.last_speech_time

            if not speech or buf_len == 0:
                continue

            now = time.monotonic()
            silence_duration = now - last_speech if last_speech > 0 else 0

            # Final result: silence exceeded threshold after speech.
            if silence_duration >= self.silence_timeout:
                await self._transcribe_and_send(is_final=True)

    async def _transcribe_and_send(self, is_final: bool):
        """Snapshot the buffer, transcribe, and send the result over WS."""
        async with self.lock:
            # Need at least ~300ms of speech to bother transcribing.
            min_bytes = int(SAMPLE_RATE * SAMPLE_WIDTH * 0.3)
            if len(self.audio_buffer) < min_bytes:
                if is_final:
                    self.audio_buffer.clear()
                    self.speech_active = False
                    reset_vad_states()
                return

            audio_data = bytes(self.audio_buffer)
            if is_final:
                self.audio_buffer.clear()
                self.speech_active = False
                reset_vad_states()

        tmp_path = None
        try:
            with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
                tmp_path = f.name
                pcm_to_wav(audio_data, tmp_path)

            # MLX is NOT thread-safe — run inference on the main thread,
            # serialized via a global lock to prevent concurrent access.
            async with _inference_lock:
                text = transcribe_audio(tmp_path)

            if text:
                result = {"text": text, "is_final": is_final}
                await self.ws.send(json.dumps(result))
                logger.info(f"{'FINAL' if is_final else 'PARTIAL'}: {text}")
            else:
                logger.debug("Transcription returned noise-only, skipping")
        except Exception as e:
            logger.error(f"Transcription error: {e}", exc_info=True)
        finally:
            if tmp_path:
                try:
                    os.unlink(tmp_path)
                except OSError:
                    pass

    def stop(self):
        self.running = False


async def handle_connection(ws):
    """Handle one WebSocket client session."""
    logger.info("New ASR session connected")
    session = ASRSession(ws)

    transcription_task = asyncio.create_task(session.run_transcription_loop())

    try:
        async for message in ws:
            if isinstance(message, bytes):
                await session.handle_audio(message)
            else:
                logger.debug(f"Text message ignored: {message[:120]}")
    except websockets.exceptions.ConnectionClosed:
        logger.info("ASR session disconnected")
    finally:
        session.stop()
        transcription_task.cancel()
        try:
            await transcription_task
        except asyncio.CancelledError:
            pass


async def main(host: str, port: int, model_name: str):
    load_vad()
    load_model(model_name)
    logger.info(f"Starting VibeVoice ASR WebSocket server on ws://{host}:{port}")

    async with websockets.serve(handle_connection, host, port, max_size=2**20):
        await asyncio.Future()  # run forever


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="VibeVoice ASR WebSocket Server")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=8200, help="Bind port")
    parser.add_argument("--model", default=None, help="Model name or path")
    parser.add_argument(
        "--silence-timeout",
        type=float,
        default=0.8,
        help="Seconds of silence before emitting a final result",
    )
    parser.add_argument(
        "--vad-threshold",
        type=float,
        default=0.5,
        help="Silero VAD speech probability threshold (0.0-1.0)",
    )
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper()),
        format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    )

    if args.model is None:
        args.model = (
            "mlx-community/VibeVoice-ASR-4bit"
            if is_apple_silicon()
            else "microsoft/VibeVoice-ASR"
        )

    asyncio.run(main(args.host, args.port, args.model))
