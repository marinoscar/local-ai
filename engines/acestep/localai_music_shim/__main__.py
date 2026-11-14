"""python -m localai_music_shim: run the shim (localai-music.service)."""

from __future__ import annotations

import logging
import signal
import sys
import threading

from .backend import make_backend
from .config import HOST, SettingsError, from_env
from .server import make_server
from .tasks import TaskManager


def main() -> int:
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s", stream=sys.stderr)
    log = logging.getLogger("localai_music_shim")
    try:
        settings = from_env()
    except SettingsError as exc:
        log.error("bad setting: %s", exc)
        return 2
    manager = TaskManager(make_backend(settings), settings.max_queue)
    srv = make_server(settings, manager)
    manager.start()
    log.info("listening on %s:%d (model=%s backend=%s)", HOST, settings.port, settings.model_id or "-", settings.backend)

    def stop(_signum: int, _frame: object) -> None:
        threading.Thread(target=srv.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        srv.serve_forever(poll_interval=0.5)
    finally:
        manager.stop()
        srv.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
