"""Settings of the shim, read from the environment (/etc/localai/music.env, written by the agent).

The names and defaults are the canonical ones of docs/architecture/overview.md section 3 (E11 contract
section 1). The shim always binds 127.0.0.1: there is no setting for the host.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Mapping

HOST = "127.0.0.1"
DEFAULT_PORT = 8082
DEFAULT_WORK_DIR = "/var/lib/localai/music/work"
DEFAULT_MAX_QUEUE = 4

DEVICES = ("cuda", "cpu")
DTYPES = ("auto", "fp16", "bf16", "fp32")
BACKENDS = ("acestep", "fake")


class SettingsError(ValueError):
    """An environment value is not valid."""


@dataclass(frozen=True)
class Settings:
    port: int = DEFAULT_PORT
    model_id: str = ""
    model_dir: str = ""
    work_dir: str = DEFAULT_WORK_DIR
    device: str = "cuda"
    dtype: str = "auto"
    max_queue: int = DEFAULT_MAX_QUEUE
    # backend is "acestep" (the real engine) or "fake" (tests, CI, a node without a GPU).
    backend: str = "acestep"


def _int(env: Mapping[str, str], name: str, default: int, lo: int, hi: int) -> int:
    raw = env.get(name, "").strip()
    if raw == "":
        return default
    try:
        value = int(raw)
    except ValueError as exc:
        raise SettingsError(f"{name} must be an integer") from exc
    if not lo <= value <= hi:
        raise SettingsError(f"{name} must be between {lo} and {hi}")
    return value


def _choice(env: Mapping[str, str], name: str, default: str, allowed: tuple[str, ...]) -> str:
    value = env.get(name, "").strip() or default
    if value not in allowed:
        raise SettingsError(f"{name} must be one of {', '.join(allowed)}")
    return value


def from_env(env: Mapping[str, str] | None = None) -> Settings:
    """Read the settings; a bad value raises SettingsError."""
    if env is None:
        env = os.environ
    work_dir = env.get("MUSIC_WORK_DIR", "").strip() or DEFAULT_WORK_DIR
    if not os.path.isabs(work_dir):
        raise SettingsError("MUSIC_WORK_DIR must be an absolute path")
    model_dir = env.get("MUSIC_MODEL_DIR", "").strip()
    if model_dir and not os.path.isabs(model_dir):
        raise SettingsError("MUSIC_MODEL_DIR must be an absolute path")
    return Settings(
        port=_int(env, "MUSIC_PORT", DEFAULT_PORT, 1, 65535),
        model_id=env.get("MUSIC_MODEL_ID", "").strip(),
        model_dir=model_dir,
        work_dir=os.path.normpath(work_dir),
        device=_choice(env, "MUSIC_DEVICE", "cuda", DEVICES),
        dtype=_choice(env, "MUSIC_DTYPE", "auto", DTYPES),
        max_queue=_int(env, "MUSIC_MAX_QUEUE", DEFAULT_MAX_QUEUE, 1, 64),
        backend=_choice(env, "MUSIC_BACKEND", "acestep", BACKENDS),
    )
