"""The loopback HTTP server between the agent and ACE-Step (E11 contract section 2).

GET /health, POST /v1/tasks, GET /v1/tasks/{id}, POST /v1/tasks/{id}/cancel. Standard library only
(http.server.ThreadingHTTPServer): the runtime venv carries ACE-Step and its wheels, nothing else.
The access log records the method, the path and the status, never a body.
"""

from __future__ import annotations

import json
import logging
import os
import re
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Optional

from .backend import GenerateRequest
from .config import HOST, Settings
from .tasks import NotReady, QueueFull, TaskManager

log = logging.getLogger("localai_music_shim")

MAX_BODY = 64 * 1024
MAX_LYRICS_BYTES = 8 * 1024
MAX_TAGS_BYTES = 512
MIN_DURATION, MAX_DURATION = 10, 240
SAMPLE_MODES = ("sounds_like", "same_song")
TASK_PATH = re.compile(r"^/v1/tasks/(tsk_[0-9a-f]{12})(/cancel)?$")
LANGUAGE = re.compile(r"^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$")
FIELDS = {
    "lyrics", "tags", "duration_s", "language", "seed", "variations",
    "sample_path", "sample_mode", "strength", "output_dir",
}


class BadRequest(Exception):
    pass


def _is_under(path: str, root: str) -> bool:
    path, root = os.path.normpath(path), os.path.normpath(root)
    return path != root and path.startswith(root.rstrip("/") + "/")


def _number(v: Any) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def parse_task(body: Any, settings: Settings) -> GenerateRequest:
    """Validate a POST /v1/tasks body. The error text names a field, never its value."""
    if not isinstance(body, dict):
        raise BadRequest("body must be a JSON object")
    unknown = sorted(set(body) - FIELDS)
    if unknown:
        raise BadRequest(f"unknown field {unknown[0]!r}")
    lyrics = body.get("lyrics", "")
    if not isinstance(lyrics, str) or len(lyrics.encode("utf-8")) > MAX_LYRICS_BYTES:
        raise BadRequest("lyrics must be a string of at most 8 KiB")
    tags = body.get("tags", "")
    if not isinstance(tags, str) or len(tags.encode("utf-8")) > MAX_TAGS_BYTES:
        raise BadRequest("tags must be a string of at most 512 bytes")
    if not lyrics.strip() and not tags.strip():
        raise BadRequest("lyrics or tags is required")
    duration = body.get("duration_s", 60)
    if not _number(duration) or not MIN_DURATION <= duration <= MAX_DURATION:
        raise BadRequest("duration_s must be between 10 and 240")
    language = body.get("language", "en")
    if not isinstance(language, str) or not LANGUAGE.match(language):
        raise BadRequest("language must be a language code such as en or es")
    seed = body.get("seed")
    if seed is not None and (not isinstance(seed, int) or isinstance(seed, bool) or not 0 <= seed < 2**32):
        raise BadRequest("seed must be null or an integer from 0 to 4294967295")
    variations = body.get("variations", 1)
    if not isinstance(variations, int) or isinstance(variations, bool) or not 1 <= variations <= 4:
        raise BadRequest("variations must be 1 to 4")
    sample_path = body.get("sample_path")
    sample_mode = body.get("sample_mode")
    strength = body.get("strength")
    if sample_path is not None:
        if not isinstance(sample_path, str) or not os.path.isabs(sample_path) or not _is_under(sample_path, settings.work_dir):
            raise BadRequest("sample_path must be an absolute path under the work directory")
        if not os.path.isfile(sample_path):
            raise BadRequest("sample_path does not exist")
        if sample_mode is None:
            sample_mode = "sounds_like"
    if sample_mode is not None and sample_mode not in SAMPLE_MODES:
        raise BadRequest("sample_mode must be sounds_like or same_song")
    if sample_mode is not None and sample_path is None:
        raise BadRequest("sample_mode needs sample_path")
    if strength is not None and (not _number(strength) or not 0.0 <= strength <= 1.0):
        raise BadRequest("strength must be between 0 and 1")
    if strength is not None and sample_path is None:
        raise BadRequest("strength needs sample_path")
    output_dir = body.get("output_dir")
    if not isinstance(output_dir, str) or not os.path.isabs(output_dir) or not _is_under(output_dir, settings.work_dir):
        raise BadRequest("output_dir must be an absolute path under the work directory")
    return GenerateRequest(
        lyrics=lyrics, tags=tags, duration_s=float(duration), language=language, seed=seed,
        variations=variations, sample_path=sample_path, sample_mode=sample_mode,
        strength=None if strength is None else float(strength), output_dir=os.path.normpath(output_dir),
    )


class Handler(BaseHTTPRequestHandler):
    server_version = "localai-music-shim"
    protocol_version = "HTTP/1.1"

    # set by make_server
    manager: TaskManager
    settings: Settings

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002 - the base class's name
        # Only the request line's method and path and the status reach the log, never a body.
        log.debug("http %s", format % args)

    def _send(self, status: int, body: Optional[dict] = None) -> None:
        raw = b"" if body is None else json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        if raw:
            self.wfile.write(raw)

    def _read_json(self) -> Any:
        length = self.headers.get("Content-Length")
        if length is None:
            raise BadRequest("Content-Length is required")
        try:
            n = int(length)
        except ValueError as exc:
            raise BadRequest("bad Content-Length") from exc
        if n < 0 or n > MAX_BODY:
            raise BadRequest("the body is larger than 64 KiB")
        raw = self.rfile.read(n)
        try:
            return json.loads(raw.decode("utf-8") or "null")
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise BadRequest("the body is not valid JSON") from exc

    def do_GET(self) -> None:  # noqa: N802 - http.server's name
        if self.path == "/health":
            h = self.manager.health()
            self._send(HTTPStatus.OK, {
                "status": h["status"], "model_id": self.settings.model_id, "device": h["device"] or self.settings.device,
                "dtype": h["dtype"] or self.settings.dtype, "vram_mib": h["vram_mib"], "error": h["error"],
            })
            return
        m = TASK_PATH.match(self.path)
        if m and not m.group(2):
            view = self.manager.get(m.group(1))
            if view is None:
                self._send(HTTPStatus.NOT_FOUND, {"error": "not_found"})
            else:
                self._send(HTTPStatus.OK, view)
            return
        self._send(HTTPStatus.NOT_FOUND, {"error": "not_found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path == "/v1/tasks":
            try:
                req = parse_task(self._read_json(), self.settings)
                task_id = self.manager.submit(req)
            except BadRequest as exc:
                self._send(HTTPStatus.BAD_REQUEST, {"error": str(exc)})
                return
            except QueueFull:
                self._send(HTTPStatus.TOO_MANY_REQUESTS, {"error": "queue_full"})
                return
            except NotReady:
                self._send(HTTPStatus.SERVICE_UNAVAILABLE, {"error": "not_ready"})
                return
            self._send(HTTPStatus.ACCEPTED, {"task_id": task_id})
            return
        m = TASK_PATH.match(self.path)
        if m and m.group(2):
            # drain a body the client may have sent
            n = int(self.headers.get("Content-Length") or 0)
            if 0 < n <= MAX_BODY:
                self.rfile.read(n)
            if self.manager.cancel(m.group(1)):
                self._send(HTTPStatus.OK, {})
            else:
                self._send(HTTPStatus.NOT_FOUND, {"error": "not_found"})
            return
        self._send(HTTPStatus.NOT_FOUND, {"error": "not_found"})


def make_server(settings: Settings, manager: TaskManager, port: Optional[int] = None) -> ThreadingHTTPServer:
    """A server bound to 127.0.0.1 (port 0 picks a free port: tests)."""
    handler = type("BoundHandler", (Handler,), {"manager": manager, "settings": settings})
    srv = ThreadingHTTPServer((HOST, settings.port if port is None else port), handler)
    srv.daemon_threads = True
    return srv
