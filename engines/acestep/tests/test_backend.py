"""Unit tests of AceStepBackend against a fake ACE-Step API (no torch, no GPU, no network)."""

from __future__ import annotations

import os
import tempfile
import threading
import types
import unittest
import wave
from dataclasses import dataclass, field
from typing import Any, Optional

from localai_music_shim import config
from localai_music_shim.backend import (
    AceStepBackend,
    BackendError,
    Cancelled,
    GenerateRequest,
    build_overlay,
    quiet_engine_log,
    secret_lines,
    write_wav,
)

LYRICS = "[verse]\nzebra-lyric-marker canta"


@dataclass
class Params:
    caption: str = ""
    lyrics: str = ""
    instrumental: bool = False
    vocal_language: str = "unknown"
    duration: float = -1.0
    inference_steps: int = 8
    shift: float = 1.0
    seed: int = -1
    task_type: str = "text2music"
    src_audio: Optional[str] = None
    reference_audio: Optional[str] = None
    audio_cover_strength: float = 1.0


@dataclass
class Config:
    batch_size: int = 2
    use_random_seed: bool = True
    seeds: Optional[list] = None
    audio_format: str = "flac"


@dataclass
class Result:
    success: bool
    audios: list = field(default_factory=list)
    error: Optional[str] = None


class FakeAPI:
    """Records every call; each generate writes a 1 s WAV like ACE-Step does (a uuid name in save_dir)."""

    def __init__(self, fail_init: str = "", fail_gen: str = "", hold: Optional[threading.Event] = None):
        self.calls: list[tuple[Params, Config]] = []
        self.inits: list[dict] = []
        self.fail_init, self.fail_gen, self.hold = fail_init, fail_gen, hold
        self.VALID_LANGUAGES = {"en", "es", "unknown"}
        self.GenerationParams, self.GenerationConfig = Params, Config
        api = self

        class Dit:
            dtype = "torch.bfloat16"

            def initialize_service(self, **kw):
                api.inits.append({"dit": kw, "ckpt": os.environ.get("ACESTEP_CHECKPOINTS_DIR")})
                if api.fail_init == "dit":
                    return "CUDA out of memory. Tried to allocate", False
                return "ok", True

        class Llm:
            def initialize(self, **kw):
                api.inits.append({"llm": kw})
                if api.fail_init == "llm":
                    return "boom", False
                return "ok", True

        self.AceStepHandler, self.LLMHandler = Dit, Llm

    def generate_music(self, dit: Any, llm: Any, params: Params, cfg: Config, save_dir: str, progress: Any) -> Result:
        self.calls.append((params, cfg))
        try:
            progress(0.1, desc="Phase 1")
            if self.hold is not None:
                while not self.hold.wait(0.01):
                    progress(0.5, desc="diffusion")
            progress(0.8, desc="Decoding audio...")
        except Exception as exc:  # noqa: BLE001 - ACE-Step catches everything and reports failure
            return Result(success=False, error=str(exc))
        if self.fail_gen:
            return Result(success=False, error=self.fail_gen)
        os.makedirs(save_dir, exist_ok=True)
        path = os.path.join(save_dir, "3f2a.wav")
        write_wav(path, 1.0)
        return Result(success=True, audios=[{"path": path, "sample_rate": 48000}])


class Rig:
    def __init__(self, api: FakeAPI, dtype: str = "auto", complete: bool = True):
        self.tmp = tempfile.TemporaryDirectory()
        self.model = os.path.join(self.tmp.name, "model")
        self.work = os.path.join(self.tmp.name, "work")
        os.makedirs(self.work)
        dirs = AceStepBackend.REQUIRED if complete else AceStepBackend.REQUIRED[:2]
        for d in dirs:
            os.makedirs(os.path.join(self.model, d))
        with open(os.path.join(self.model, AceStepBackend.DIT, "model.safetensors"), "wb") as f:
            f.write(b"weights")
        with open(os.path.join(self.model, AceStepBackend.DIT, "modeling.py"), "w") as f:
            f.write("# upstream code\n")
        self.settings = config.Settings(model_id="acestep-v15-turbo", model_dir=self.model, work_dir=self.work, device="cuda", dtype=dtype)
        self.api = api
        self.backend = AceStepBackend(self.settings, api=lambda: api)
        self.progress: list[float] = []

    def req(self, name: str = "t1", **over: Any) -> GenerateRequest:
        base = dict(lyrics=LYRICS, tags="latin pop", duration_s=60.0, language="es", seed=7, variations=2,
                    sample_path=None, sample_mode=None, strength=None, output_dir=os.path.join(self.work, name))
        base.update(over)
        return GenerateRequest(**base)

    def close(self) -> None:
        self.tmp.cleanup()


class AceStepBackendTests(unittest.TestCase):
    def setUp(self) -> None:
        self._env = dict(os.environ)

    def tearDown(self) -> None:
        os.environ.clear()
        os.environ.update(self._env)

    def rig(self, *a: Any, **kw: Any) -> Rig:
        r = Rig(*a, **kw)
        self.addCleanup(r.close)
        return r

    def test_load_uses_a_writable_overlay_and_stays_offline(self):
        r = self.rig(FakeAPI())
        info = r.backend.load()
        self.assertEqual(info.dtype, "bf16")
        overlay = os.path.join(r.work, AceStepBackend.OVERLAY)
        self.assertEqual(r.api.inits[0]["ckpt"], overlay)
        self.assertEqual(os.environ.get("ACESTEP_PROJECT_ROOT"), overlay)  # ACE-Step's .cache lands in work/
        self.assertEqual(r.api.inits[0]["dit"]["config_path"], "acestep-v15-turbo")
        self.assertEqual(r.api.inits[1]["llm"]["checkpoint_dir"], overlay)
        self.assertEqual(r.api.inits[1]["llm"]["lm_model_path"], "acestep-5Hz-lm-1.7B")
        # the 12 GB card needs both CPU offloads (spike #505)
        self.assertEqual((r.api.inits[0]["dit"]["offload_to_cpu"], r.api.inits[0]["dit"]["offload_dit_to_cpu"]), (True, True))
        self.assertTrue(r.api.inits[1]["llm"]["offload_to_cpu"])
        weights = os.path.join(overlay, AceStepBackend.DIT, "model.safetensors")
        code = os.path.join(overlay, AceStepBackend.DIT, "modeling.py")
        self.assertTrue(os.path.islink(weights))
        self.assertFalse(os.path.islink(code))
        self.assertEqual(os.environ.get("HF_HUB_OFFLINE"), "1")
        self.assertEqual(os.environ.get("TRANSFORMERS_OFFLINE"), "1")

    def test_overlay_never_writes_through_to_the_model(self):
        r = self.rig(FakeAPI())
        dst = os.path.join(r.work, "ovl")
        build_overlay(r.model, dst)
        with open(os.path.join(dst, AceStepBackend.DIT, "modeling.py"), "w") as f:
            f.write("# package code\n")
        with open(os.path.join(r.model, AceStepBackend.DIT, "modeling.py")) as f:
            self.assertEqual(f.read(), "# upstream code\n")
        build_overlay(r.model, dst)  # rebuilt from scratch at the next load
        with open(os.path.join(dst, AceStepBackend.DIT, "modeling.py")) as f:
            self.assertEqual(f.read(), "# upstream code\n")

    def test_load_refusals(self):
        for name, rig, want in [
            ("incomplete model", lambda: self.rig(FakeAPI(), complete=False), "incomplete"),
            ("dtype not auto", lambda: self.rig(FakeAPI(), dtype="fp16"), "MUSIC_DTYPE"),
            ("dit oom", lambda: self.rig(FakeAPI(fail_init="dit")), "out of graphics memory"),
            ("lm fails", lambda: self.rig(FakeAPI(fail_init="llm")), "language model"),
        ]:
            with self.subTest(name):
                with self.assertRaisesRegex(BackendError, want):
                    rig().backend.load()

    def test_runtime_missing(self):
        r = self.rig(FakeAPI())

        def boom() -> Any:
            raise ImportError("no acestep")

        r.backend = AceStepBackend(r.settings, api=boom)
        with self.assertRaisesRegex(BackendError, "not installed"):
            r.backend.load()

    def test_text2music_two_variations(self):
        r = self.rig(FakeAPI())
        r.backend.load()
        outs = r.backend.generate(r.req(), r.progress.append, lambda: False)
        self.assertEqual([os.path.basename(o.path) for o in outs], ["out-0.wav", "out-1.wav"])
        self.assertEqual(sorted(os.listdir(r.req().output_dir)), ["out-0.wav", "out-1.wav"])
        for o in outs:
            self.assertEqual((o.sample_rate, o.mime), (48000, "audio/wav"))
            with wave.open(o.path) as w:
                self.assertEqual(w.getframerate(), 48000)
        (p0, c0), (p1, c1) = r.api.calls
        self.assertEqual((p0.seed, p1.seed), (7, 8))
        self.assertEqual((c0.seeds, c0.use_random_seed, c0.batch_size, c0.audio_format), ([7], False, 1, "wav"))
        self.assertEqual((p0.task_type, p0.vocal_language, p0.duration, p0.inference_steps, p0.shift), ("text2music", "es", 60.0, 8, 3.0))
        self.assertIsNone(p0.reference_audio)
        self.assertEqual(r.progress[-1], 1.0)
        self.assertEqual(r.progress, sorted(r.progress))

    def test_sample_modes(self):
        r = self.rig(FakeAPI())
        r.backend.load()
        sample = os.path.join(r.work, "sample.wav")
        write_wav(sample, 1.0)
        r.backend.generate(r.req("a", variations=1, sample_path=sample, sample_mode="same_song", strength=0.4), r.progress.append, lambda: False)
        r.backend.generate(r.req("b", variations=1, sample_path=sample, sample_mode="sounds_like", strength=0.5), r.progress.append, lambda: False)
        r.backend.generate(r.req("c", variations=1, sample_path=sample, sample_mode="same_song"), r.progress.append, lambda: False)
        cover, ref, cover_default = (c[0] for c in r.api.calls)
        self.assertEqual((cover.task_type, cover.src_audio, cover.audio_cover_strength), ("cover", sample, 0.4))
        self.assertEqual((ref.task_type, ref.reference_audio, ref.src_audio), ("text2music", sample, None))
        self.assertEqual(cover_default.audio_cover_strength, 1.0)

    def test_instrumental_random_seed_and_language(self):
        r = self.rig(FakeAPI())
        r.backend.load()
        r.backend.generate(r.req(lyrics="  ", seed=None, variations=1, language="en"), r.progress.append, lambda: False)
        p, c = r.api.calls[0]
        self.assertEqual((p.lyrics, p.instrumental, p.seed, c.use_random_seed, c.seeds), ("[Instrumental]", True, -1, True, None))
        with self.assertRaisesRegex(BackendError, "language"):
            r.backend.generate(r.req("x", language="tlh"), r.progress.append, lambda: False)

    def test_engine_failure_is_short_and_lyric_free(self):
        r = self.rig(FakeAPI(fail_gen="bad thing with " + LYRICS))
        r.backend.load()
        with self.assertRaises(BackendError) as ctx:
            r.backend.generate(r.req(), r.progress.append, lambda: False)
        self.assertNotIn("zebra", str(ctx.exception))
        r2 = self.rig(FakeAPI(fail_gen="CUDA out of memory"))
        r2.backend.load()
        with self.assertRaisesRegex(BackendError, "out of graphics memory"):
            r2.backend.generate(r2.req(), r2.progress.append, lambda: False)

    def test_cancel_while_running(self):
        hold = threading.Event()
        r = self.rig(FakeAPI(hold=hold))
        r.backend.load()
        stop = threading.Event()
        errors: list[BaseException] = []

        def run() -> None:
            try:
                r.backend.generate(r.req(), r.progress.append, stop.is_set)
            except BaseException as exc:  # noqa: BLE001
                errors.append(exc)

        t = threading.Thread(target=run)
        t.start()
        for _ in range(500):  # wait until the first variation is inside ACE-Step
            if r.api.calls:
                break
            threading.Event().wait(0.01)
        stop.set()
        t.join(5)
        self.assertFalse(t.is_alive())
        self.assertEqual(len(errors), 1)
        self.assertIsInstance(errors[0], Cancelled)
        self.assertEqual(len(r.api.calls), 1)  # the second variation never started

    def test_engine_log_never_carries_lyrics_or_tags(self):
        sink: list[tuple[str, str]] = []

        class FakeLoguru:
            def __init__(self):
                self.removed = False
                self.level = None
                self.filter = None

            def remove(self):
                self.removed = True

            def add(self, _sink, level, filter, format, backtrace, diagnose):  # noqa: A002 - loguru's names
                self.level, self.filter = level, filter
                # never a traceback with local variables (they hold the lyrics)
                assert (backtrace, diagnose) == (False, False)
                assert callable(format) and "{exception}" not in format({})

            def emit(self, message):
                if self.filter({"message": message}):
                    sink.append(("stderr", message))

        log = FakeLoguru()
        api = FakeAPI()
        api.logger = log
        hold = threading.Event()
        api.hold = hold
        r = self.rig(api)
        r.backend.load()
        self.assertTrue(log.removed)
        self.assertEqual(log.level, "WARNING")

        def emit_during_song(v, desc=None):
            if hold.is_set():
                return
            log.emit("prompt: zebra-lyric-marker canta\nmore")
            log.emit("caption=latin pop")
            log.emit("[tiled_decode] Reduced overlap from 64 to 32")
            hold.set()

        t = threading.Thread(target=r.backend.generate, args=(r.req(variations=1), emit_during_song, lambda: False))
        t.start()
        t.join(5)
        self.assertEqual(sink, [("stderr", "[tiled_decode] Reduced overlap from 64 to 32")])
        # once the song ends, nothing is secret any more (no lyrics kept)
        self.assertEqual(r.backend._secrets, ())

    def test_secret_lines(self):
        self.assertEqual(secret_lines("[verse]\nhola mundo\n\nab\n", " folk, warm "), ("hola mundo", "folk, warm"))
        self.assertEqual(secret_lines("", ""), ())

    def test_generate_before_load(self):
        r = self.rig(FakeAPI())
        with self.assertRaisesRegex(BackendError, "not loaded"):
            r.backend.generate(r.req(), r.progress.append, lambda: False)


if __name__ == "__main__":
    unittest.main()
