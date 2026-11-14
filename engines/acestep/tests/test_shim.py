"""Unit tests of the shim: python -m unittest discover -s tests (from engines/acestep)."""

from __future__ import annotations

import http.client
import io
import json
import logging
import os
import tempfile
import threading
import time
import unittest
import wave

from localai_music_shim import config
from localai_music_shim.backend import FakeBackend
from localai_music_shim.server import make_server
from localai_music_shim.tasks import TaskManager

SECRET_LYRICS = "[verse]\nzebra-lyric-marker sings tonight"


def wait_for(fn, timeout=5.0):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        v = fn()
        if v:
            return v
        time.sleep(0.01)
    raise AssertionError("condition not met in time")


class Rig:
    def __init__(self, backend: FakeBackend, max_queue: int = 2, background_load: bool = False):
        self.tmp = tempfile.TemporaryDirectory()
        self.work = os.path.join(self.tmp.name, "work")
        os.makedirs(self.work)
        self.settings = config.Settings(port=0, model_id="music-example", work_dir=self.work, device="cpu", max_queue=max_queue, backend="fake")
        self.backend = backend
        self.manager = TaskManager(backend, max_queue)
        self.srv = make_server(self.settings, self.manager, port=0)
        self.port = self.srv.server_address[1]
        self.thread = threading.Thread(target=self.srv.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
        self.thread.start()
        self.manager.start(load_in_background=background_load)

    def close(self):
        if self.backend.gate is not None:
            self.backend.gate.set()
        self.manager.stop()
        self.srv.shutdown()
        self.srv.server_close()
        self.tmp.cleanup()

    def call(self, method, path, body=None):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        raw = None if body is None else json.dumps(body).encode()
        headers = {"Content-Type": "application/json"} if raw is not None else {}
        conn.request(method, path, body=raw, headers=headers)
        resp = conn.getresponse()
        data = resp.read()
        conn.close()
        return resp.status, (json.loads(data) if data else None)

    def task_body(self, name="t1", **over):
        body = {"lyrics": SECRET_LYRICS, "tags": "latin pop", "duration_s": 30, "language": "es", "seed": 7,
                "variations": 2, "sample_path": None, "sample_mode": None, "strength": None,
                "output_dir": os.path.join(self.work, name)}
        body.update(over)
        return body


class HealthTests(unittest.TestCase):
    def test_states(self):
        for name, backend, want in [
            ("ready", FakeBackend(), "ready"),
            ("error", FakeBackend(fail_load=True), "error"),
        ]:
            with self.subTest(name):
                rig = Rig(backend)
                try:
                    status, body = rig.call("GET", "/health")
                    self.assertEqual(status, 200)
                    self.assertEqual(body["status"], want)
                    self.assertEqual(body["model_id"], "music-example")
                    self.assertIn("vram_mib", body)
                    if want == "error":
                        self.assertTrue(body["error"])
                    else:
                        self.assertIsNone(body["error"])
                finally:
                    rig.close()

    def test_loading_then_ready(self):
        rig = Rig(FakeBackend(load_delay=0.3), background_load=True)
        try:
            _, body = rig.call("GET", "/health")
            self.assertEqual(body["status"], "loading")
            status, _ = rig.call("POST", "/v1/tasks", rig.task_body())
            self.assertEqual(status, 503)
            wait_for(lambda: rig.call("GET", "/health")[1]["status"] == "ready")
        finally:
            rig.close()


class TaskTests(unittest.TestCase):
    def test_happy_path_writes_valid_wavs(self):
        rig = Rig(FakeBackend())
        try:
            status, body = rig.call("POST", "/v1/tasks", rig.task_body())
            self.assertEqual(status, 202)
            tid = body["task_id"]
            view = wait_for(lambda: (lambda v: v if v["state"] == "done" else None)(rig.call("GET", f"/v1/tasks/{tid}")[1]))
            self.assertEqual(view["progress"], 1.0)
            self.assertEqual(len(view["outputs"]), 2)
            for out in view["outputs"]:
                self.assertEqual(out["mime"], "audio/wav")
                self.assertRegex(os.path.basename(out["path"]), r"^out-[01]\.wav$")
                with wave.open(out["path"], "rb") as w:
                    self.assertGreater(w.getnframes(), 0)
        finally:
            rig.close()

    def test_queue_full(self):
        # MUSIC_MAX_QUEUE counts the running task too: with 3, one runs, two wait, the fourth is refused.
        gate = threading.Event()
        rig = Rig(FakeBackend(gate=gate), max_queue=3)
        try:
            ids = []
            first = rig.call("POST", "/v1/tasks", rig.task_body("a"))[1]["task_id"]
            wait_for(lambda: rig.call("GET", f"/v1/tasks/{first}")[1]["state"] == "running")
            for name in ("b", "c"):
                status, body = rig.call("POST", "/v1/tasks", rig.task_body(name))
                self.assertEqual(status, 202)
                ids.append(body["task_id"])
            status, body = rig.call("POST", "/v1/tasks", rig.task_body("d"))
            self.assertEqual((status, body), (429, {"error": "queue_full"}))
            gate.set()
            self.assertTrue(rig.manager.wait_idle())
        finally:
            rig.close()

    def test_cancel_running_and_queued(self):
        gate = threading.Event()
        rig = Rig(FakeBackend(gate=gate))
        try:
            running = rig.call("POST", "/v1/tasks", rig.task_body("a"))[1]["task_id"]
            wait_for(lambda: rig.call("GET", f"/v1/tasks/{running}")[1]["state"] == "running")
            queued = rig.call("POST", "/v1/tasks", rig.task_body("b"))[1]["task_id"]
            self.assertEqual(rig.call("POST", f"/v1/tasks/{queued}/cancel")[0], 200)
            self.assertEqual(rig.call("GET", f"/v1/tasks/{queued}")[1]["state"], "cancelled")
            self.assertEqual(rig.call("POST", f"/v1/tasks/{running}/cancel")[0], 200)
            view = wait_for(lambda: (lambda v: v if v["state"] == "cancelled" else None)(rig.call("GET", f"/v1/tasks/{running}")[1]))
            self.assertEqual(view["outputs"], [])
            self.assertFalse(os.path.exists(os.path.join(rig.work, "a")))
            self.assertEqual(rig.call("POST", "/v1/tasks/tsk_000000000000/cancel")[0], 404)
        finally:
            rig.close()

    def test_failure_leaves_no_files(self):
        rig = Rig(FakeBackend(fail=True))
        try:
            tid = rig.call("POST", "/v1/tasks", rig.task_body("f"))[1]["task_id"]
            view = wait_for(lambda: (lambda v: v if v["state"] == "failed" else None)(rig.call("GET", f"/v1/tasks/{tid}")[1]))
            self.assertTrue(view["error"])
            self.assertNotIn("zebra", view["error"])
            self.assertFalse(os.path.exists(os.path.join(rig.work, "f")))
        finally:
            rig.close()

    def test_bad_requests(self):
        rig = Rig(FakeBackend())
        try:
            outside = tempfile.gettempdir()
            os.makedirs(os.path.join(rig.work, "exists"))
            cases = {
                "unknown field": rig.task_body(extra=1),
                "duration too long": rig.task_body(duration_s=241),
                "variations": rig.task_body(variations=5),
                "lyrics too long": rig.task_body(lyrics="x" * 9000),
                "output outside work": rig.task_body(output_dir=os.path.join(outside, "x")),
                "missing sample": rig.task_body(sample_path=os.path.join(rig.work, "nope.wav")),
                "mode without sample": rig.task_body(sample_mode="same_song"),
                "strength out of range": rig.task_body(strength=2),
                "bool seed": rig.task_body(seed=True),
                "seed too large": rig.task_body(seed=2**31),
                "output exists": rig.task_body(output_dir=rig.work + "/exists"),
            }
            for name, body in cases.items():
                with self.subTest(name):
                    status, reply = rig.call("POST", "/v1/tasks", body)
                    self.assertEqual(status, 400, reply)
                    self.assertNotIn("zebra", reply["error"])
            self.assertEqual(rig.call("GET", "/v1/tasks/tsk_000000000000")[0], 404)
        finally:
            rig.close()

    def test_sample_is_passed_through(self):
        backend = FakeBackend()
        rig = Rig(backend)
        try:
            sample = os.path.join(rig.work, "op1-sample.wav")
            with open(sample, "wb") as f:
                f.write(b"RIFF")
            status, _ = rig.call("POST", "/v1/tasks", rig.task_body(sample_path=sample, sample_mode="same_song", strength=1.0))
            self.assertEqual(status, 202)
            self.assertTrue(rig.manager.wait_idle())
            req = backend.calls[-1]
            self.assertEqual((req.sample_path, req.sample_mode, req.strength), (sample, "same_song", 1.0))
        finally:
            rig.close()

    def test_lyrics_never_logged(self):
        buf = io.StringIO()
        handler = logging.StreamHandler(buf)
        logger = logging.getLogger("localai_music_shim")
        logger.addHandler(handler)
        old = logger.level
        logger.setLevel(logging.DEBUG)
        rig = Rig(FakeBackend(fail=True))
        try:
            rig.call("POST", "/v1/tasks", rig.task_body())
            self.assertTrue(rig.manager.wait_idle())
            rig.call("POST", "/v1/tasks", rig.task_body(variations=9))
        finally:
            rig.close()
            logger.removeHandler(handler)
            logger.setLevel(old)
        text = buf.getvalue()
        self.assertIn("queued", text)
        self.assertNotIn("zebra", text)


class ConfigTests(unittest.TestCase):
    def test_defaults_and_errors(self):
        s = config.from_env({})
        self.assertEqual((s.port, s.max_queue, s.work_dir, s.backend), (8082, 4, "/var/lib/localai/music/work", "acestep"))
        for env in ({"MUSIC_PORT": "x"}, {"MUSIC_MAX_QUEUE": "0"}, {"MUSIC_DEVICE": "tpu"}, {"MUSIC_WORK_DIR": "rel"}, {"MUSIC_BACKEND": "other"}):
            with self.subTest(env):
                with self.assertRaises(config.SettingsError):
                    config.from_env(env)

    def test_binds_loopback(self):
        settings = config.Settings(port=0, backend="fake")
        srv = make_server(settings, TaskManager(FakeBackend(), 1), port=0)
        try:
            self.assertEqual(srv.server_address[0], "127.0.0.1")
        finally:
            srv.server_close()


if __name__ == "__main__":
    unittest.main()
