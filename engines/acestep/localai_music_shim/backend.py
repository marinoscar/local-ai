"""The one module that calls ACE-Step (E11 contract section 2).

Everything else in the shim (the HTTP server, the queue, cancellation, health) is engine-agnostic and
talks to a Backend. Two backends exist:

- FakeBackend writes a short, valid 16-bit PCM WAV per variation. MUSIC_BACKEND=fake selects it; the
  unit tests and CI use it, and so can a node without a GPU to check the plumbing.
- AceStepBackend calls ACE-Step 1.5's Python API in-process (acestep.inference.generate_music), the
  interface the spike (#505) chose over upstream's REST server and CLI. Findings and measurements:
  docs/source/acestep-spike.md.
"""

from __future__ import annotations

import contextlib
import io
import math
import os
import shutil
import sys
import struct
import threading
import time
import wave
from dataclasses import dataclass, field
from typing import Any, Callable, Optional, Protocol

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
    """The real engine: ACE-Step 1.5 (turbo DiT + 5 Hz LM 1.7B), driven through its Python API.

    MUSIC_MODEL_DIR holds the Hugging Face repo ACE-Step/Ace-Step1.5 verbatim (acestep-v15-turbo/, vae/,
    Qwen3-Embedding-0.6B/, acestep-5Hz-lm-1.7B/), read-only. ACE-Step overwrites the model's *.py files
    with its own package's copy at every load, so the backend points it at a writable overlay inside
    MUSIC_WORK_DIR (symlinks to the weights, copies of the *.py files), rebuilt at every load.

    Sample modes: same_song is ACE-Step's "cover" task (src_audio, audio_cover_strength = strength);
    sounds_like is text2music with reference_audio (a global timbre/style reference; ACE-Step has no
    strength for it, so strength is ignored in that mode). Progress is ACE-Step's own 0..1 callback per
    variation; cancel is checked at every progress call (about every 0.5 s) and between variations.

    Memory: on a 12 GB card the LM (nano-vllm) and the DiT only fit next to a sample's encoding with
    both CPU offloads on (measured by the spike: peak about 10.4 GiB, 5 GiB while idle).

    Privacy: ACE-Step logs the whole prompt (caption and lyrics) at INFO. The backend keeps its log at
    WARNING and drops any line that carries a lyric line or the tags of the running request; anything
    it prints to stdout during a song is discarded.
    """

    # The sub-folders of the Hugging Face repo the engine needs; the DiT and LM names are ACE-Step's.
    DIT = "acestep-v15-turbo"
    LM = "acestep-5Hz-lm-1.7B"
    REQUIRED = (DIT, LM, "vae", "Qwen3-Embedding-0.6B")
    # Turbo settings measured by the spike: 8 steps, shift 3.0 (guidance is fixed to 1.0 by ACE-Step).
    STEPS = 8
    SHIFT = 3.0
    OVERLAY = "_model"
    OFFLOAD_TO_CPU = True
    OFFLOAD_DIT_TO_CPU = True

    def __init__(self, settings: Settings, api: Optional[Callable[[], Any]] = None) -> None:
        self.settings = settings
        self._api_loader = api or _import_acestep
        self._api: Any = None
        self._dit: Any = None
        self._llm: Any = None
        self._dtype = "auto"
        self._secrets: tuple[str, ...] = ()

    def load(self) -> LoadInfo:
        model_dir = self.settings.model_dir
        if not model_dir or not os.path.isdir(model_dir):
            raise BackendError("the model directory does not exist")
        missing = [d for d in self.REQUIRED if not os.path.isdir(os.path.join(model_dir, d))]
        if missing:
            # Never let ACE-Step download what is missing: the engine runs offline.
            raise BackendError(f"the model directory is incomplete ({missing[0]} is missing)")
        if self.settings.dtype != "auto":
            raise BackendError("ACE-Step picks its own dtype; MUSIC_DTYPE must be auto")
        for name in ("HF_HUB_OFFLINE", "TRANSFORMERS_OFFLINE", "HF_DATASETS_OFFLINE", "HF_HUB_DISABLE_TELEMETRY"):
            os.environ.setdefault(name, "1")
        os.environ.setdefault("GRADIO_ANALYTICS_ENABLED", "False")
        try:
            self._api = self._api_loader()
        except Exception as exc:  # noqa: BLE001 - any import failure means "runtime not installed"
            raise BackendError("the ACE-Step runtime is not installed") from exc
        engine_log = getattr(self._api, "logger", None)
        if engine_log is not None:
            quiet_engine_log(engine_log, lambda: self._secrets)
        ckpt = os.path.join(self.settings.work_dir, self.OVERLAY)
        try:
            build_overlay(model_dir, ckpt)
        except OSError as exc:
            raise BackendError(f"the model overlay could not be written ({type(exc).__name__})") from exc
        os.environ["ACESTEP_CHECKPOINTS_DIR"] = ckpt
        # ACE-Step keeps small caches (progress estimates) under <project root>/.cache, by default the working
        # directory, which is read-only under the unit; keep everything it writes inside the overlay.
        os.environ["ACESTEP_PROJECT_ROOT"] = ckpt
        device = self.settings.device
        try:
            self._dit = self._api.AceStepHandler()
            msg, ok = self._dit.initialize_service(
                project_root=ckpt, config_path=self.DIT, device=device,
                offload_to_cpu=self.OFFLOAD_TO_CPU, offload_dit_to_cpu=self.OFFLOAD_DIT_TO_CPU,
            )
            if not ok:
                raise BackendError(_short_error(msg, "the music model could not be loaded"))
            self._llm = self._api.LLMHandler()
            msg, ok = self._llm.initialize(checkpoint_dir=ckpt, lm_model_path=self.LM, backend="vllm", device=device,
                                           offload_to_cpu=self.OFFLOAD_TO_CPU)
            if not ok:
                raise BackendError(_short_error(msg, "the music language model could not be loaded"))
        except BackendError:
            raise
        except Exception as exc:  # noqa: BLE001
            raise BackendError(_short_error(str(exc), f"the model could not be loaded ({type(exc).__name__})")) from exc
        self._dtype = _dtype_name(getattr(self._dit, "dtype", None))
        return LoadInfo(device=device, dtype=self._dtype, vram_mib=_cuda_vram_mib())

    def generate(self, req: GenerateRequest, progress: ProgressFn, cancelled: CancelledFn) -> list[Output]:
        if self._dit is None or self._llm is None:
            raise BackendError("the model is not loaded")
        api = self._api
        language = req.language.split("-", 1)[0].lower()
        if language not in api.VALID_LANGUAGES:
            raise BackendError("the model does not sing this language")
        instrumental = not req.lyrics.strip()
        self._secrets = secret_lines(req.lyrics, req.tags)
        try:
            return self._generate(req, progress, cancelled, language, instrumental)
        finally:
            self._secrets = ()

    def _generate(self, req: GenerateRequest, progress: ProgressFn, cancelled: CancelledFn,
                  language: str, instrumental: bool) -> list[Output]:
        api = self._api
        os.makedirs(req.output_dir, exist_ok=True)
        outputs: list[Output] = []
        n = req.variations
        for v in range(n):
            if cancelled():
                raise Cancelled()
            seed = -1 if req.seed is None else req.seed + v
            fields: dict[str, Any] = {
                "caption": req.tags,
                "lyrics": "[Instrumental]" if instrumental else req.lyrics,
                "instrumental": instrumental,
                "vocal_language": language,
                "duration": float(req.duration_s),
                "inference_steps": self.STEPS,
                "shift": self.SHIFT,
                "seed": seed,
            }
            if req.sample_path and req.sample_mode == "same_song":
                fields.update(task_type="cover", src_audio=req.sample_path,
                              audio_cover_strength=1.0 if req.strength is None else float(req.strength))
            elif req.sample_path:
                fields["reference_audio"] = req.sample_path
            params = api.GenerationParams(**fields)
            config = api.GenerationConfig(batch_size=1, use_random_seed=req.seed is None,
                                          seeds=None if req.seed is None else [seed], audio_format="wav")

            def step(value: Any = 0.0, *_args: Any, _v: int = v, **_kw: Any) -> None:
                if cancelled():
                    raise Cancelled()
                try:
                    frac = max(0.0, min(1.0, float(value)))
                except (TypeError, ValueError):
                    return
                progress((_v + frac) / n)

            scratch = os.path.join(req.output_dir, f".v{v}")
            try:
                with contextlib.redirect_stdout(io.StringIO()):
                    result = api.generate_music(self._dit, self._llm, params, config, save_dir=scratch, progress=step)
            except Cancelled:
                raise
            except Exception as exc:  # noqa: BLE001
                raise BackendError(_short_error(str(exc), f"the engine failed ({type(exc).__name__})")) from exc
            if cancelled():
                raise Cancelled()
            if not getattr(result, "success", False) or not result.audios:
                raise BackendError(_short_error(getattr(result, "error", "") or "", "the engine failed"))
            audio = result.audios[0]
            src = audio.get("path") or ""
            if not os.path.isfile(src):
                raise BackendError("the engine wrote no audio file")
            path = os.path.join(req.output_dir, f"out-{v}.wav")
            os.replace(src, path)
            shutil.rmtree(scratch, ignore_errors=True)
            rate = int(audio.get("sample_rate") or _wav_rate(path))
            outputs.append(Output(path=path, duration_s=_audio_seconds(path, req.duration_s), sample_rate=rate))
            progress((v + 1) / n)
        return outputs


def _import_acestep() -> Any:
    """ACE-Step's API as one namespace (imported lazily: torch takes seconds and needs the venv)."""
    import types

    from acestep.constants import VALID_LANGUAGES  # type: ignore[import-not-found]
    from acestep.handler import AceStepHandler  # type: ignore[import-not-found]
    from acestep.inference import GenerationConfig, GenerationParams, generate_music  # type: ignore[import-not-found]
    from acestep.llm_inference import LLMHandler  # type: ignore[import-not-found]
    from loguru import logger  # type: ignore[import-not-found]  # ACE-Step's logger

    return types.SimpleNamespace(
        AceStepHandler=AceStepHandler, LLMHandler=LLMHandler, GenerationParams=GenerationParams,
        GenerationConfig=GenerationConfig, generate_music=generate_music, VALID_LANGUAGES=set(VALID_LANGUAGES),
        logger=logger,
    )


def secret_lines(lyrics: str, tags: str) -> tuple[str, ...]:
    """The strings no log line may carry: every lyric line of 4+ characters (section tags excluded) and the tags."""
    out = [ln.strip() for ln in lyrics.splitlines() if len(ln.strip()) >= 4 and not ln.strip().startswith("[")]
    if tags.strip():
        out.append(tags.strip())
    return tuple(out)


def quiet_engine_log(logger: Any, secrets: Callable[[], tuple[str, ...]]) -> None:
    """Replace loguru's default sink: WARNING and above to stderr, one line per record, without any line that
    carries a secret. No tracebacks: loguru's default "diagnose" traceback prints local variables, and the
    request (with its lyrics) is one of them."""

    def keep(record: Any) -> bool:
        message = str(record["message"])
        return not any(s in message for s in secrets())

    def line(_record: Any) -> str:
        # A callable format: loguru does not append the exception to it.
        return "{level} acestep {name}:{line}: {message}\n"

    logger.remove()
    logger.add(sys.stderr, level="WARNING", filter=keep, format=line, backtrace=False, diagnose=False)


def build_overlay(src: str, dst: str) -> None:
    """A writable view of the read-only model directory: every file a symlink, except *.py files, which are
    copied because ACE-Step replaces them with its package's version at load. Rebuilt from scratch."""
    shutil.rmtree(dst, ignore_errors=True)
    for root, _dirs, files in os.walk(src):
        rel = os.path.relpath(root, src)
        os.makedirs(os.path.join(dst, rel), exist_ok=True)
        for name in files:
            a, b = os.path.join(root, name), os.path.normpath(os.path.join(dst, rel, name))
            if name.endswith(".py"):
                shutil.copyfile(a, b)
            else:
                os.symlink(a, b)


def _short_error(text: str, fallback: str) -> str:
    """A short, lyric-free reason for /health and the task: a known cause, else the fallback."""
    low = (text or "").lower()
    if "out of memory" in low or "cuda oom" in low:
        return "out of graphics memory"
    if "no such file" in low or "not found" in low:
        return fallback + " (a model file is missing)"
    return fallback


def _dtype_name(dtype: Any) -> str:
    name = str(dtype or "").replace("torch.", "")
    return {"float16": "fp16", "bfloat16": "bf16", "float32": "fp32"}.get(name, name or "auto")


def _audio_seconds(path: str, fallback: float) -> float:
    try:
        import soundfile  # type: ignore[import-not-found]  # in the runtime venv (an ACE-Step dependency)

        info = soundfile.info(path)
        return round(info.frames / float(info.samplerate), 3)
    except Exception:  # noqa: BLE001 - fall back to the stdlib reader (PCM only)
        return _wav_seconds(path, fallback)


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
    """GPU memory in use on the device once loaded (total minus free, as nvidia-smi shows it)."""
    try:
        import torch  # type: ignore[import-not-found]

        if torch.cuda.is_available():
            free, total = torch.cuda.mem_get_info()
            return int((total - free) // (1024 * 1024))
    except Exception:  # noqa: BLE001
        return None
    return None


def make_backend(settings: Settings) -> Backend:
    if settings.backend == "fake":
        return FakeBackend(seconds=1.0, steps=10, step_delay=0.05)
    return AceStepBackend(settings)
