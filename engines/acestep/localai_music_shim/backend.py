"""The one module that calls ACE-Step (E11 contract section 2).

Everything else in the shim (the HTTP server, the queue, cancellation, health) is engine-agnostic and
talks to a Backend. Two backends exist:

- FakeBackend writes a short, valid 16-bit PCM WAV per variation. MUSIC_BACKEND=fake selects it; the
  unit tests and CI use it, and so can a node without a GPU to check the plumbing.
- AceStepBackend wraps the ACE-Step Python package. The exact 1.5 API is not known until the spike runs
  on a real node (#505); every assumption is marked TODO(#505).
"""

from __future__ import annotations

import math
import os
import struct
import threading
import time
import wave
from dataclasses import dataclass, field
from typing import Callable, Optional, Protocol

from .config import Settings


class Cancelled(Exception):
    """The task was cancelled while the backend ran."""


class BackendError(Exception):
    """The backend failed. The message is short and never contains lyrics."""


@dataclass(frozen=True)
class LoadInfo:
    device: str
    dtype: str
    vram_mib: Optional[int] = None


@dataclass(frozen=True)
class GenerateRequest:
    lyrics: str
    tags: str
    duration_s: float
    language: str
    seed: Optional[int]
    variations: int
    sample_path: Optional[str]
    sample_mode: Optional[str]
    strength: Optional[float]
    output_dir: str


@dataclass(frozen=True)
class Output:
    path: str
    duration_s: float
    sample_rate: int
    mime: str = "audio/wav"


ProgressFn = Callable[[float], None]
CancelledFn = Callable[[], bool]


class Backend(Protocol):
    def load(self) -> LoadInfo:
        """Load the model. Called once, from a background thread, before any generate."""

    def generate(self, req: GenerateRequest, progress: ProgressFn, cancelled: CancelledFn) -> list[Output]:
        """Make req.variations songs in req.output_dir. Raise Cancelled when cancelled() turns true."""


def write_wav(path: str, seconds: float, sample_rate: int = 48000, freq: float = 440.0) -> float:
    """Write a mono 16-bit PCM WAV with a quiet sine tone; returns its duration in seconds."""
    frames = max(1, int(seconds * sample_rate))
    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sample_rate)
        chunk = bytearray()
        for i in range(frames):
            chunk += struct.pack("<h", int(3000 * math.sin(2 * math.pi * freq * i / sample_rate)))
        w.writeframes(bytes(chunk))
    return frames / sample_rate


@dataclass
class FakeBackend:
    """A backend for tests: no model, no GPU.

    steps: progress reports per variation; step_delay: seconds between them; fail: raise BackendError
    from generate; fail_load: raise from load; gate: when set, generate waits for it before writing
    (tests use it to hold a task in the running state).
    """

    seconds: float = 0.25
    steps: int = 4
    step_delay: float = 0.0
    fail: bool = False
    fail_load: bool = False
    load_delay: float = 0.0
    gate: Optional[threading.Event] = None
    device: str = "cpu"
    dtype: str = "fp32"
    calls: list[GenerateRequest] = field(default_factory=list)

    def load(self) -> LoadInfo:
        if self.load_delay:
            time.sleep(self.load_delay)
        if self.fail_load:
            raise BackendError("the fake model could not be loaded")
        return LoadInfo(device=self.device, dtype=self.dtype, vram_mib=None)

    def generate(self, req: GenerateRequest, progress: ProgressFn, cancelled: CancelledFn) -> list[Output]:
        self.calls.append(req)
        if self.gate is not None:
            while not self.gate.wait(0.01):
                if cancelled():
                    raise Cancelled()
        if self.fail:
            raise BackendError("the fake engine failed")
        os.makedirs(req.output_dir, exist_ok=True)
        outputs: list[Output] = []
        total = req.variations * self.steps
        done = 0
        for v in range(req.variations):
            for _ in range(self.steps):
                if cancelled():
                    raise Cancelled()
                if self.step_delay:
                    time.sleep(self.step_delay)
                done += 1
                progress(done / total)
            path = os.path.join(req.output_dir, f"out-{v}.wav")
            dur = write_wav(path, self.seconds, freq=440.0 + 110.0 * v)
            outputs.append(Output(path=path, duration_s=round(dur, 3), sample_rate=48000))
        return outputs


class AceStepBackend:
    """The real engine. TODO(#505): every call below is a best-effort reading of ACE-Step's Python API
    and must be checked against the version the spike pins (the wheel bundle of
    kit/engines/acestep/<version>/)."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self._pipe = None
        self._dtype = "fp32"

    def _torch_dtype_name(self) -> str:
        # TODO(#505): confirm the dtype names the pipeline accepts.
        mapping = {"fp16": "float16", "bf16": "bfloat16", "fp32": "float32"}
        if self.settings.dtype == "auto":
            return "bfloat16" if self.settings.device == "cuda" else "float32"
        return mapping[self.settings.dtype]

    def load(self) -> LoadInfo:
        if not self.settings.model_dir or not os.path.isdir(self.settings.model_dir):
            raise BackendError("the model directory does not exist")
        try:
            # TODO(#505): ACE-Step 1.5 module and class names. 1.0 exposed
            # acestep.pipeline_ace_step.ACEStepPipeline(checkpoint_dir, dtype, torch_compile).
            from acestep.pipeline_ace_step import ACEStepPipeline  # type: ignore[import-not-found]
        except Exception as exc:  # noqa: BLE001 - any import failure means "runtime not installed"
            raise BackendError("the ACE-Step runtime is not installed") from exc
        dtype = self._torch_dtype_name()
        try:
            self._pipe = ACEStepPipeline(checkpoint_dir=self.settings.model_dir, dtype=dtype, torch_compile=False)
            # TODO(#505): 1.0 loads lazily on the first call; force the load here if 1.5 allows it.
            loader = getattr(self._pipe, "load_checkpoint", None)
            if callable(loader):
                loader(self.settings.model_dir)
        except Exception as exc:  # noqa: BLE001
            raise BackendError(f"the model could not be loaded ({type(exc).__name__})") from exc
        self._dtype = {"float16": "fp16", "bfloat16": "bf16", "float32": "fp32"}.get(dtype, dtype)
        return LoadInfo(device=self.settings.device, dtype=self._dtype, vram_mib=_cuda_vram_mib())

    def generate(self, req: GenerateRequest, progress: ProgressFn, cancelled: CancelledFn) -> list[Output]:
        if self._pipe is None:
            raise BackendError("the model is not loaded")
        os.makedirs(req.output_dir, exist_ok=True)
        outputs: list[Output] = []
        for v in range(req.variations):
            if cancelled():
                raise Cancelled()
            seed = None if req.seed is None else req.seed + v
            path = os.path.join(req.output_dir, f"out-{v}.wav")
            kwargs = {
                # TODO(#505): parameter names and defaults of the 1.5 call (1.0 names shown).
                "audio_duration": float(req.duration_s),
                "prompt": req.tags,
                "lyrics": req.lyrics,
                "infer_step": 60,
                "guidance_scale": 15.0,
                "manual_seeds": [seed] if seed is not None else None,
                "save_path": path,
                "format": "wav",
            }
            if req.sample_path:
                # TODO(#505): "same_song" maps to the repaint/edit task, "sounds_like" to audio2audio
                # with a reference strength. Confirm both against the 1.5 task list.
                kwargs["audio2audio_enable"] = True
                kwargs["ref_audio_input"] = req.sample_path
                kwargs["ref_audio_strength"] = float(req.strength if req.strength is not None else 0.5)
            try:
                self._pipe(**kwargs)
            except Exception as exc:  # noqa: BLE001
                raise BackendError(f"the engine failed ({type(exc).__name__})") from exc
            progress((v + 1) / req.variations)
            if not os.path.isfile(path):
                raise BackendError("the engine wrote no audio file")
            outputs.append(Output(path=path, duration_s=_wav_seconds(path, req.duration_s), sample_rate=_wav_rate(path)))
        return outputs


def _wav_seconds(path: str, fallback: float) -> float:
    try:
        with wave.open(path, "rb") as w:
            return round(w.getnframes() / float(w.getframerate()), 3)
    except Exception:  # noqa: BLE001 - not a PCM wav (float wav): report what was asked
        return float(fallback)


def _wav_rate(path: str) -> int:
    try:
        with wave.open(path, "rb") as w:
            return w.getframerate()
    except Exception:  # noqa: BLE001
        return 48000


def _cuda_vram_mib() -> Optional[int]:
    try:
        import torch  # type: ignore[import-not-found]

        if torch.cuda.is_available():
            return int(torch.cuda.memory_allocated() // (1024 * 1024))
    except Exception:  # noqa: BLE001
        return None
    return None


def make_backend(settings: Settings) -> Backend:
    if settings.backend == "fake":
        return FakeBackend(seconds=1.0, steps=10, step_delay=0.05)
    return AceStepBackend(settings)
