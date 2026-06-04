#!/usr/bin/env python3
"""End-to-end smoke test: does the TalosRed proxy route a request upstream?

Stdlib only — no pip install. Spins up a fake OpenAI-compatible upstream,
builds and runs the talosred binary, points a model alias at the fake
upstream, fires a chat request through the proxy, and verifies the proxy
forwarded it and returned the upstream's answer.

Run:  python3 tests/proxy_smoke_test.py
Exits 0 on success, non-zero on failure.
"""

import json
import os
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PROXY_PORT = 18099
DUMMY_KEY = "sk-talos-local"
ALIAS_MODEL = "gpt-smoke-test"

UPSTREAM_REPLY = {
    "id": "chatcmpl-fake",
    "object": "chat.completion",
    "created": 1,
    "model": "llama-smoke",
    "choices": [
        {
            "index": 0,
            "message": {"role": "assistant", "content": "pong from upstream"},
            "finish_reason": "stop",
        }
    ],
    "usage": {"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
}


class FakeUpstream(BaseHTTPRequestHandler):
    received = None  # captured request body, for assertions

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        FakeUpstream.received = self.rfile.read(n)
        body = json.dumps(UPSTREAM_REPLY).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass  # silence


def start_upstream():
    srv = HTTPServer(("127.0.0.1", 0), FakeUpstream)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"


def post(path, data, headers=None, form=False):
    url = f"http://127.0.0.1:{PROXY_PORT}{path}"
    if form:
        body = data.encode()  # data is already a urlencoded string
        hdr = {"Content-Type": "application/x-www-form-urlencoded"}
    else:
        body = json.dumps(data).encode()
        hdr = {"Content-Type": "application/json"}
    if headers:
        hdr.update(headers)
    req = urllib.request.Request(url, data=body, headers=hdr, method="POST")
    with urllib.request.urlopen(req, timeout=10) as resp:
        return resp.status, resp.read().decode()


def wait_healthy(timeout=15):
    deadline = time.time() + timeout
    url = f"http://127.0.0.1:{PROXY_PORT}/health"
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1) as resp:
                if resp.status == 200:
                    return True
        except (urllib.error.URLError, ConnectionError):
            time.sleep(0.2)
    return False


def fail(msg):
    print(f"FAIL: {msg}")
    sys.exit(1)


def main():
    upstream, upstream_url = start_upstream()
    print(f"fake upstream at {upstream_url}")

    tmp = tempfile.mkdtemp(prefix="talos-smoke-")
    binary = os.path.join(tmp, "talosred")

    print("building binary...")
    build = subprocess.run(
        ["go", "build", "-o", binary, "."],
        cwd=REPO_ROOT, capture_output=True, text=True,
    )
    if build.returncode != 0:
        fail(f"go build failed:\n{build.stderr}")

    env = dict(os.environ, OPENAI_API_KEY="sk-real-stays-server-side")
    proc = subprocess.Popen(
        [binary, "--port", str(PROXY_PORT),
         "--db-path", os.path.join(tmp, "smoke.db"),
         "--hooks-dir", os.path.join(tmp, "no-hooks"),
         "--proxy-key", DUMMY_KEY],
        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )

    try:
        if not wait_healthy():
            fail("proxy did not become healthy")
        print("proxy healthy")

        # 1. Route ALIAS_MODEL to the fake upstream (transparent aliasing).
        alias_form = (
            f"pattern={ALIAS_MODEL}&target_model=llama-smoke"
            f"&target_url={upstream_url}&provider=openai"
        )
        status, _ = post("/ui/aliases", alias_form, form=True)
        if status != 200:
            fail(f"alias create returned {status}")
        print("alias created")

        # 2. Wrong proxy key must be rejected (key vaulting).
        try:
            post("/v1/chat/completions",
                 {"model": ALIAS_MODEL, "messages": [{"role": "user", "content": "hi"}]},
                 headers={"Authorization": "Bearer WRONG"})
            fail("wrong proxy key was not rejected")
        except urllib.error.HTTPError as e:
            if e.code != 401:
                fail(f"wrong key: expected 401, got {e.code}")
        print("wrong key rejected (401)")

        # 3. Correct key — proxy should forward to upstream and return its reply.
        status, body = post(
            "/v1/chat/completions",
            {"model": ALIAS_MODEL, "messages": [{"role": "user", "content": "ping"}]},
            headers={"Authorization": f"Bearer {DUMMY_KEY}", "X-Talos-App": "smoke"},
        )
        if status != 200:
            fail(f"chat returned {status}: {body}")

        reply = json.loads(body)
        content = reply["choices"][0]["message"]["content"]
        if content != "pong from upstream":
            fail(f"unexpected content: {content!r}")
        print(f"proxy forwarded and returned upstream reply: {content!r}")

        # 4. Upstream must have actually received the (rewritten) request.
        if FakeUpstream.received is None:
            fail("upstream never received the forwarded request")
        sent = json.loads(FakeUpstream.received)
        if sent.get("model") != "llama-smoke":
            fail(f"alias did not rewrite model: {sent.get('model')!r}")
        print("upstream received rewritten model 'llama-smoke'")

        print("\nPASS: proxy redirect verified")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        upstream.shutdown()


if __name__ == "__main__":
    main()
