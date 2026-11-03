"""Windows-side soak for the Python edge client.

Runs the client against a pure-Python mock upstream for N hours, hitting
it with a buffered request every 10s and a streaming one every 60s, and
samples the client's memory / handle count every 60s.

Unlike the WSL run this exercises the Windows code path too, and it
survives WSL being reclaimed between turns.

Usage:  python clients/python/tests/soak_win.py [hours]
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
# .../llmRx/clients/python/tests/soak_win.py -> repo root is parents[3]
REPO = HERE.parents[3]
WORK = Path(os.environ.get("SOAK_WORK", REPO.parent / "llmrx-soak-win"))
HOURS = float(sys.argv[1]) if len(sys.argv) > 1 else float(
    os.environ.get("SOAK_HOURS", "4"))
CLI_PORT = 18973
MOCK_PORT = 18972
PROGRESS = WORK / "soak_progress.log"

WORK.mkdir(parents=True, exist_ok=True)


def http(method: str, url: str, body: bytes | None = None,
         headers: dict | None = None, timeout: float = 30) -> tuple[int, bytes]:
    req = urllib.request.Request(url, data=body, method=method,
                                 headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        try:
            return exc.code, exc.read()
        finally:
            exc.close()
    except OSError:
        return 0, b""


def client_rss_kb(pid: int) -> int:
    """Working set of the client process, in KB (Windows)."""
    try:
        import ctypes
        from ctypes import wintypes

        class PROCESS_MEMORY_COUNTERS(ctypes.Structure):
            _fields_ = [
                ("cb", wintypes.DWORD),
                ("PageFaultCount", wintypes.DWORD),
                ("PeakWorkingSetSize", ctypes.c_size_t),
                ("WorkingSetSize", ctypes.c_size_t),
                ("QuotaPeakPagedPoolUsage", ctypes.c_size_t),
                ("QuotaPagedPoolUsage", ctypes.c_size_t),
                ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t),
                ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
                ("PagefileUsage", ctypes.c_size_t),
                ("PeakPagefileUsage", ctypes.c_size_t),
            ]

        counters = PROCESS_MEMORY_COUNTERS()
        counters.cb = ctypes.sizeof(counters)
        handle = ctypes.windll.kernel32.OpenProcess(0x1000, False, pid)
        if not handle:
            return 0
        try:
            ctypes.windll.psapi.GetProcessMemoryInfo(
                handle, ctypes.byref(counters), counters.cb)
            return counters.WorkingSetSize // 1024
        finally:
            ctypes.windll.kernel32.CloseHandle(handle)
    except Exception:  # noqa: BLE001 — diagnostics must never kill the soak
        return 0


def handle_count(pid: int) -> int:
    """Open handle count for the client process (Windows).

    GetProcessHandleCount needs the process handle opened with
    PROCESS_QUERY_LIMITED_INFORMATION; a self-handle would count the
    harness's own handles instead.
    """
    try:
        import ctypes

        class HANDLE_COUNT(ctypes.Structure):
            _fields_ = [("dwHandleCount", ctypes.c_ulong)]

        PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
        handle = ctypes.windll.kernel32.OpenProcess(
            PROCESS_QUERY_LIMITED_INFORMATION, False, pid)
        if not handle:
            return -1
        try:
            hc = HANDLE_COUNT()
            hc.dwHandleCount = 0xFFFFFFFF
            ok = ctypes.windll.kernel32.GetProcessHandleCount(
                handle, ctypes.byref(hc))
            return int(hc.dwHandleCount) if ok else -1
        finally:
            ctypes.windll.kernel32.CloseHandle(handle)
    except Exception:  # noqa: BLE001
        return -1


def main() -> int:
    mock = subprocess.Popen(
        [sys.executable, str(HERE / "mock_upstream_win.py"), str(MOCK_PORT)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(1.5)

    cfg = WORK / "client.yml"
    cfg.write_text(
        f"listen: 127.0.0.1:{CLI_PORT}\n"
        "servers:\n"
        f"  - http://127.0.0.1:{MOCK_PORT}\n"
        "cache_enabled: true\n"
        "cache_ttl_sec: 300\n"
        "fallback_mode: cache_then_fail\n"
        f"usage_file: {WORK / 'usage.jsonl'}\n"
        "usage_upload_sec: 3600\n",
        encoding="utf-8",
    )

    env = dict(os.environ)
    env["PYTHONPATH"] = str(REPO / "clients" / "python" / "src")
    client = subprocess.Popen(
        [sys.executable, "-u", "-m", "llmrx_client", "-config", str(cfg)],
        stdout=open(WORK / "client.log", "w", encoding="utf-8"),
        stderr=subprocess.STDOUT,
        env=env,
    )

    try:
        for _ in range(60):
            status, _ = http("GET", f"http://127.0.0.1:{CLI_PORT}/healthz",
                             timeout=3)
            if status == 200:
                break
            time.sleep(0.5)

        body = json.dumps({"model": "gpt-4o-mini", "temperature": 0,
                           "messages": [{"role": "user", "content": "soak"}]}).encode()
        stream = json.dumps({"model": "gpt-4o-mini", "stream": True,
                             "messages": [{"role": "user",
                                           "content": "soak"}]}).encode()
        hdr = {"Content-Type": "application/json"}

        end = time.time() + HOURS * 3600
        ok = fail = n = 0
        with open(PROGRESS, "a", encoding="utf-8") as log:
            log.write(f"soak start {time.strftime('%H:%M:%S')} "
                      f"duration={HOURS}h pid={client.pid} platform=windows\n")
            log.flush()
            while time.time() < end:
                n += 1
                payload = stream if n % 10 == 0 else body
                status, _ = http("POST",
                                 f"http://127.0.0.1:{CLI_PORT}/v1/chat/completions",
                                 payload, hdr)
                if status == 200:
                    ok += 1
                else:
                    fail += 1
                    log.write(f"{time.strftime('%H:%M:%S')} REQUEST FAIL "
                              f"status={status}\n")
                if n % 6 == 0:
                    rss = client_rss_kb(client.pid)
                    handles = handle_count(client.pid)
                    log.write(f"{time.strftime('%H:%M:%S')} n={n} ok={ok} "
                              f"fail={fail} rss_kb={rss} handles={handles}\n")
                    log.flush()
                time.sleep(10)
            log.write(f"soak end {time.strftime('%H:%M:%S')} ok={ok} fail={fail}\n")
            status, body = http("GET", f"http://127.0.0.1:{CLI_PORT}/healthz")
            log.write(f"final healthz: {body.decode()}\n")
            log.flush()
        print(f"soak finished: n={n} ok={ok} fail={fail}")
        return 0
    finally:
        for proc in (client, mock):
            try:
                proc.terminate()
                proc.wait(timeout=10)
            except Exception:  # noqa: BLE001
                pass


if __name__ == "__main__":
    raise SystemExit(main())
