"""A tiny Python service for rig: JSON logs on stdout, Prometheus text on /metrics, no dependencies."""
import json
import os
import random
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

counts = {"requests": 0, "failures": 0}
latency = {"sum": 0.0, "count": 0}
lock = threading.Lock()


def log(level, msg, **fields):
    print(json.dumps({"time": time.strftime("%Y-%m-%dT%H:%M:%S"), "level": level, "msg": msg, **fields}), flush=True)


def greet(name):
    if not name:
        raise ValueError("name is required")
    return f"hello, {name}"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlparse(self.path)
        if url.path == "/healthz":
            return self.reply(200, "ok")
        if url.path == "/metrics":
            return self.reply(200, metrics(), "text/plain; version=0.0.4")
        if url.path != "/hello":
            return self.reply(404, "not found")
        start = time.monotonic()
        name = parse_qs(url.query).get("name", [""])[0]
        time.sleep(random.uniform(0.001, 0.02))
        try:
            if random.random() < 0.02:
                raise RuntimeError("unlucky")
            self.reply(200, greet(name))
            ok = True
        except Exception as e:
            log("error", "hello failed", name=name, err=str(e))
            self.reply(500, str(e))
            ok = False
        took = time.monotonic() - start
        with lock:
            counts["requests"] += 1
            counts["failures"] += 0 if ok else 1
            latency["sum"] += took
            latency["count"] += 1
        log("info", "hello", name=name, ms=round(took * 1000, 2))

    def reply(self, code, body, kind="text/plain"):
        data = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_):
        pass


def metrics():
    with lock:
        return (
            f"pyhello_requests_total {counts['requests']}\n"
            f"pyhello_failures_total {counts['failures']}\n"
            f"pyhello_latency_seconds_sum {latency['sum']}\n"
            f"pyhello_latency_seconds_count {latency['count']}\n"
        )


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "7080"))
    log("info", "listening", port=port, python=sys.version.split()[0])
    ThreadingHTTPServer(("", port), Handler).serve_forever()
