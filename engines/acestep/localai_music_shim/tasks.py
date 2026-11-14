"""The task queue: a bounded FIFO and one worker thread (E11 contract section 2).

States: queued -> running -> done | failed | cancelled; queued -> cancelled. A full queue (max_queue
tasks waiting, the running one not counted) refuses a new task. Lyrics are kept only in memory, only
until the task ends, and never logged.
"""

from __future__ import annotations

import collections
import logging
import os
import secrets
import shutil
import threading
from dataclasses import dataclass, field
from typing import Optional

from .backend import Backend, BackendError, Cancelled, GenerateRequest, LoadInfo, Output

log = logging.getLogger("localai_music_shim")

QUEUED, RUNNING, DONE, FAILED, CANCELLED = "queued", "running", "done", "failed", "cancelled"
FINAL = (DONE, FAILED, CANCELLED)

# How many finished tasks are remembered for GET /v1/tasks/{id}.
KEEP_FINISHED = 64


class QueueFull(Exception):
    pass


class NotReady(Exception):
    pass


@dataclass
class Task:
    id: str
    req: Optional[GenerateRequest]
    state: str = QUEUED
    progress: Optional[float] = None
    error: Optional[str] = None
    outputs: list[Output] = field(default_factory=list)
    cancel: threading.Event = field(default_factory=threading.Event)

    def view(self) -> dict:
        return {
            "state": self.state,
            "progress": self.progress,
            "error": self.error,
            "outputs": [
                {"path": o.path, "duration_s": o.duration_s, "sample_rate": o.sample_rate, "mime": o.mime}
                for o in self.outputs
            ],
        }


def new_task_id() -> str:
    return "tsk_" + secrets.token_hex(6)


class TaskManager:
    def __init__(self, backend: Backend, max_queue: int) -> None:
        self.backend = backend
        self.max_queue = max_queue
        self._lock = threading.Condition()
        self._queue: collections.deque[Task] = collections.deque()
        self._tasks: dict[str, Task] = {}
        self._finished: collections.deque[str] = collections.deque()
        self._running: Optional[Task] = None
        self._stopping = False
        self.status = "loading"
        self.load_info: Optional[LoadInfo] = None
        self.load_error: Optional[str] = None
        self._worker = threading.Thread(target=self._work, name="music-worker", daemon=True)

    # -- lifecycle ---------------------------------------------------------------------------------

    def start(self, load_in_background: bool = True) -> None:
        if load_in_background:
            threading.Thread(target=self._load, name="music-load", daemon=True).start()
        else:
            self._load()
        self._worker.start()

    def _load(self) -> None:
        try:
            info = self.backend.load()
        except Exception as exc:  # noqa: BLE001 - any failure is reported on /health
            with self._lock:
                self.status = "error"
                self.load_error = str(exc) if isinstance(exc, BackendError) else f"load failed ({type(exc).__name__})"
            log.error("model load failed: %s", self.load_error)
            return
        with self._lock:
            self.status = "ready"
            self.load_info = info
            self._lock.notify_all()
        log.info("model ready (device=%s dtype=%s)", info.device, info.dtype)

    def stop(self, timeout: float = 5.0) -> None:
        with self._lock:
            self._stopping = True
            for t in self._queue:
                t.state = CANCELLED
            self._queue.clear()
            if self._running is not None:
                self._running.cancel.set()
            self._lock.notify_all()
        if self._worker.is_alive():
            self._worker.join(timeout)

    # -- API ---------------------------------------------------------------------------------------

    def health(self) -> dict:
        with self._lock:
            info = self.load_info
            return {
                "status": self.status,
                "error": self.load_error,
                "device": info.device if info else None,
                "dtype": info.dtype if info else None,
                "vram_mib": info.vram_mib if info else None,
            }

    def submit(self, req: GenerateRequest) -> str:
        with self._lock:
            if self.status != "ready" or self._stopping:
                raise NotReady()
            # max_queue counts the running task and the queued ones (music.md section 3.4).
            if len(self._queue) + (1 if self._running else 0) >= self.max_queue:
                raise QueueFull()
            task = Task(id=new_task_id(), req=req)
            self._tasks[task.id] = task
            self._queue.append(task)
            self._lock.notify_all()
        log.info(
            "task %s queued (lyrics_bytes=%d variations=%d duration_s=%s sample=%s)",
            task.id, len(req.lyrics.encode("utf-8")), req.variations, req.duration_s, bool(req.sample_path),
        )
        return task.id

    def get(self, task_id: str) -> Optional[dict]:
        with self._lock:
            t = self._tasks.get(task_id)
            return None if t is None else t.view()

    def cancel(self, task_id: str) -> bool:
        """Cancel a task; False when it is unknown. A finished task stays as it is."""
        with self._lock:
            t = self._tasks.get(task_id)
            if t is None:
                return False
            if t.state == QUEUED:
                self._queue.remove(t)
                self._finish(t, CANCELLED)
            elif t.state == RUNNING:
                t.cancel.set()
            return True

    def counts(self) -> tuple[int, int]:
        with self._lock:
            return (1 if self._running else 0), len(self._queue)

    # -- worker ------------------------------------------------------------------------------------

    def _finish(self, t: Task, state: str, error: Optional[str] = None) -> None:
        # caller holds the lock
        t.state = state
        t.error = error
        t.req = None  # drop the lyrics
        self._finished.append(t.id)
        while len(self._finished) > KEEP_FINISHED:
            self._tasks.pop(self._finished.popleft(), None)
        self._lock.notify_all()

    def _work(self) -> None:
        while True:
            with self._lock:
                while not self._stopping and not self._queue:
                    self._lock.wait()
                if self._stopping:
                    return
                t = self._queue.popleft()
                t.state = RUNNING
                t.progress = 0.0
                self._running = t
                req = t.req
            assert req is not None

            def progress(p: float, t: Task = t) -> None:
                with self._lock:
                    t.progress = max(0.0, min(1.0, float(p)))

            state, error, outputs = DONE, None, []
            try:
                outputs = self.backend.generate(req, progress, t.cancel.is_set)
                if t.cancel.is_set():
                    raise Cancelled()
            except Cancelled:
                state = CANCELLED
            except BackendError as exc:
                state, error = FAILED, str(exc)
            except Exception as exc:  # noqa: BLE001 - never let the worker die
                state, error = FAILED, f"internal error ({type(exc).__name__})"
            if state != DONE:
                _remove_tree(req.output_dir)
                outputs = []
                log.warning("task %s %s%s", t.id, state, f": {error}" if error else "")
            else:
                log.info("task %s done (%d outputs)", t.id, len(outputs))
            with self._lock:
                t.outputs = outputs
                if state == DONE:
                    t.progress = 1.0
                self._running = None
                self._finish(t, state, error)

    def wait_idle(self, timeout: float = 5.0) -> bool:
        """Tests: wait until nothing is queued or running."""
        with self._lock:
            return self._lock.wait_for(lambda: not self._queue and self._running is None, timeout)


def _remove_tree(path: str) -> None:
    if path and os.path.isdir(path):
        shutil.rmtree(path, ignore_errors=True)
