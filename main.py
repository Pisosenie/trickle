import asyncio
import json
import os
import signal
import urllib.error
import urllib.request

import decky

API_URL = "http://127.0.0.1:47610"
BINARY = os.path.join(decky.DECKY_PLUGIN_DIR, "bin", "torrentd")
STATE_DIR = decky.DECKY_PLUGIN_RUNTIME_DIR
TOKEN_PATH = os.path.join(STATE_DIR, "api-token")
METHODS = {"GET", "POST", "PUT", "DELETE"}
REQUEST_TIMEOUT = 10
MAX_BACKOFF = 30
MAX_RELAY_BACKOFF = 5
NOT_RUNNING = {"success": False, "error": "Download service is not running"}


def _token():
    try:
        with open(TOKEN_PATH) as f:
            return f.read().strip()
    except OSError:
        return ""


def _open(path, method="GET", body=None, timeout=REQUEST_TIMEOUT):
    headers = {"Authorization": "Bearer " + _token()}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(API_URL + path, data=data, method=method, headers=headers)
    return urllib.request.urlopen(req, timeout=timeout)


def _request(method, path, body):
    try:
        with _open(path, method, body) as res:
            return json.load(res)
    except urllib.error.HTTPError as err:
        try:
            return json.load(err)
        except ValueError:
            return {"success": False, "error": f"HTTP {err.code}"}
    except OSError:
        return NOT_RUNNING


class Plugin:
    async def api(self, method, path, body=None):
        if method not in METHODS or not path.startswith("/api/v1/") or ".." in path:
            return {"success": False, "error": "bad request"}
        return await asyncio.to_thread(_request, method, path, body)

    async def set_game(self, running):
        self.game = bool(running)
        await asyncio.to_thread(self._send_game)

    def _send_game(self):
        if self.game is not None:
            _request("PUT", "/api/v1/game", {"running": self.game})

    async def _main(self):
        self.loop = asyncio.get_running_loop()
        self.stopping = False
        self.proc = None
        self.game = None
        self.tasks = [
            self.loop.create_task(self._supervise()),
            self.loop.create_task(self._relay_events()),
        ]

    async def _unload(self):
        self.stopping = True
        for task in self.tasks:
            task.cancel()
        await self._stop_daemon()

    async def _uninstall(self):
        pass

    async def _supervise(self):
        backoff = 1
        while not self.stopping:
            started = self.loop.time()
            try:
                code = await self._run_daemon()
                decky.logger.warning(f"torrentd exited with {code}")
            except OSError as err:
                decky.logger.error(f"cannot start torrentd: {err}")
            if self.loop.time() - started > 60:
                backoff = 1
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, MAX_BACKOFF)

    async def _run_daemon(self):
        os.chmod(BINARY, 0o755)
        log_path = os.path.join(decky.DECKY_PLUGIN_LOG_DIR, "torrentd.log")
        env = dict(os.environ, HOME=decky.DECKY_USER_HOME)
        game = "false" if self.game is False else "true"
        with open(log_path, "ab") as log:
            self.proc = await asyncio.create_subprocess_exec(
                BINARY,
                "-state-dir", STATE_DIR,
                "-exit-with-parent",
                f"-game-running={game}",
                stdin=asyncio.subprocess.PIPE,
                stdout=log,
                stderr=log,
                env=env,
            )
            decky.logger.info(f"torrentd started, pid {self.proc.pid}")
            return await self.proc.wait()

    async def _stop_daemon(self):
        proc = self.proc
        if proc is None or proc.returncode is not None:
            return
        proc.send_signal(signal.SIGTERM)
        try:
            await asyncio.wait_for(proc.wait(), timeout=10)
        except asyncio.TimeoutError:
            proc.kill()
            await proc.wait()

    async def _relay_events(self):
        backoff = 1
        while not self.stopping:
            try:
                await asyncio.to_thread(self._read_events)
                backoff = 1
            except OSError:
                pass
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, MAX_RELAY_BACKOFF)

    def _read_events(self):
        with _open("/api/v1/events", timeout=None) as stream:
            self._send_game()
            for raw in stream:
                if self.stopping:
                    return
                line = raw.decode(errors="replace").strip()
                if not line.startswith("data: "):
                    continue
                try:
                    event = json.loads(line[len("data: "):])
                except ValueError:
                    continue
                if event.get("type") == "torrent_completed":
                    name = (event.get("data") or {}).get("name", "")
                    asyncio.run_coroutine_threadsafe(decky.emit("torrent_completed", name), self.loop)
