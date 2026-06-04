#!/usr/bin/env python3
"""Fire traffic at a RUNNING TalosRed instance and confirm it lands in the logs.

Unlike proxy_smoke_test.py, this does NOT build or start the binary — it hits
an instance you already have running (default http://localhost:8080). It spins
up a local fake upstream, points an alias at it, sends a batch of requests with
different X-Talos-App / X-Talos-User tags, then reads /ui/requests and /ui/usage
back to prove the calls were logged and attributed.

Usage:
  ./talosred                       # in another terminal
  python3 tests/traffic_check.py
  python3 tests/traffic_check.py --base http://localhost:8080 --proxy-key sk-talos-local --count 6

Exits 0 if the traffic shows up in the logs, non-zero otherwise.
"""

import argparse
import json
import re
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

ALIAS = "traffic-check-model"

# (app, user) pairs to spread the generated traffic across.
TAGS = [("billing-service", "dave"), ("search-api", "alice"), ("billing-service", "carol")]


class FakeUpstream(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        self.rfile.read(n)
        reply = {
            "id": "chatcmpl-traffic",
            "object": "chat.completion",
            "created": 1,
            "model": "llama-local",
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": "ack"}}],
            "usage": {"prompt_tokens": 42, "completion_tokens": 7, "total_tokens": 49},
        }
        body = json.dumps(reply).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


def http(method, url, data=None, headers=None, form=False, timeout=10):
    hdr = dict(headers or {})
    body = None
    if data is not None:
        if form:
            body = data.encode()
            hdr.setdefault("Content-Type", "application/x-www-form-urlencoded")
        else:
            body = json.dumps(data).encode()
            hdr.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=body, headers=hdr, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status, resp.read().decode()


def fail(msg):
    print(f"FAIL: {msg}")
    sys.exit(1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://localhost:8080")
    ap.add_argument("--proxy-key", default="sk-talos-local",
                    help="dummy key, if the instance was started with --proxy-key")
    ap.add_argument("--count", type=int, default=6)
    args = ap.parse_args()
    base = args.base.rstrip("/")

    # 0. instance reachable?
    try:
        http("GET", f"{base}/health", timeout=3)
    except (urllib.error.URLError, ConnectionError) as e:
        fail(f"no TalosRed at {base} — start the binary first ({e})")
    print(f"instance up at {base}")

    # local fake upstream
    upstream = HTTPServer(("127.0.0.1", 0), FakeUpstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    upstream_url = f"http://127.0.0.1:{upstream.server_address[1]}"

    auth = {"Authorization": f"Bearer {args.proxy_key}"}

    try:
        # 1. point the alias at the fake upstream (so no real keys are needed)
        try:
            http("POST", f"{base}/ui/aliases",
                 data=(f"pattern={ALIAS}&target_model=llama-local"
                       f"&target_url={upstream_url}&provider=openai"),
                 form=True)
        except urllib.error.HTTPError as e:
            fail(f"could not create alias (auth?): {e.code}")
        print(f"alias {ALIAS} -> {upstream_url}")

        # 2. generate traffic
        sent = 0
        for i in range(args.count):
            app, user = TAGS[i % len(TAGS)]
            try:
                status, _ = http(
                    "POST", f"{base}/v1/chat/completions",
                    data={"model": ALIAS, "messages": [{"role": "user", "content": f"ping {i}"}]},
                    headers={**auth, "X-Talos-App": app, "X-Talos-User": user},
                )
            except urllib.error.HTTPError as e:
                fail(f"request {i} rejected ({e.code}) — wrong --proxy-key?")
            if status != 200:
                fail(f"request {i} returned {status}")
            sent += 1
        print(f"sent {sent} requests across apps {sorted({a for a, _ in TAGS})}")

        # 3. confirm they show up in the logs (async write — poll briefly)
        ids = []
        for _ in range(50):
            _, html = http("GET", f"{base}/ui/requests?app=billing-service")
            ids = re.findall(r'data-id="(talos-[0-9a-f-]+)"', html)
            if len(ids) >= 4:  # billing-service = indices 0,2,3,5 of 6
                break
            time.sleep(0.1)
        billing_expected = sum(1 for i in range(args.count) if TAGS[i % len(TAGS)][0] == "billing-service")
        if len(ids) < billing_expected:
            fail(f"logs show {len(ids)} billing-service rows, expected {billing_expected}")
        print(f"logs show {len(ids)} billing-service requests")

        # check the model + attribution actually rendered in the row HTML
        if ALIAS not in html and "llama-local" not in html:
            fail("logged rows missing the routed model")
        if "billing-service" not in html:
            fail("attribution app not rendered in logs")

        # 4. usage page aggregates the spend by app
        _, usage = http("GET", f"{base}/ui/usage")
        for app in {a for a, _ in TAGS}:
            if app not in usage:
                fail(f"usage page missing app {app!r}")
        print("usage page shows per-app attribution")

        print("\nPASS: generated traffic is visible in logs + usage")
        print(f"      open {base}/ui to see it")
    finally:
        # tidy up the alias we created
        try:
            http("DELETE", f"{base}/ui/aliases/{ALIAS}")
        except Exception:
            pass
        upstream.shutdown()


if __name__ == "__main__":
    main()
