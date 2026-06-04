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
import concurrent.futures
import json
import re
import random
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ALIAS = "traffic-check-model"
TARGET_MODEL = (
    "llama-local"  # alias rewrites ALIAS -> this; needs pricing for $ to show
)

# (app, user) pairs to spread the generated traffic across.
TAGS = [
    ("billing-service", "dave"),
    ("billing-service", "alice"),
    ("search-api", "alice"),
    ("search-api", "dave"),
    ("search-api", "carol"),
]


class FakeUpstream(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        self.rfile.read(n)
        reply = {
            "id": "chatcmpl-traffic",
            "object": "chat.completion",
            "created": 1,
            "model": "llama-local",
            "choices": [
                {
                    "index": 0,
                    "finish_reason": "stop",
                    "message": {"role": "assistant", "content": "ack"},
                }
            ],
            "usage": {"prompt_tokens": 42, "completion_tokens": 7, "total_tokens": 49},
        }
        self.pause()
        body = json.dumps(reply).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass

    def pause(self):
        tx = random.uniform(0.1, 1)
        time.sleep(tx)


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


def parse_usage_counts(html):
    """Map attribution key -> request count from the /ui/usage tables.

    Each row renders the key in <span class="attr">KEY</span> followed by the
    Requests cell <td class="tok">N</td>. Covers both the By-App and By-User
    tables; callers look up the keys they care about.
    """
    pairs = re.findall(
        r'<span class="attr">([^<]+)</span>.*?<td class="tok">(\d+)</td>',
        html,
        re.S,
    )
    return {key: int(n) for key, n in pairs}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://localhost:8080")
    ap.add_argument(
        "--proxy-key",
        default="sk-talos-local",
        help="dummy key, if the instance was started with --proxy-key",
    )
    ap.add_argument("--count", type=int, default=6)
    ap.add_argument(
        "--workers",
        type=int,
        default=4,
        help="concurrent senders (clamped to 1-10)",
    )
    args = ap.parse_args()
    base = args.base.rstrip("/")
    workers = max(1, min(10, args.workers))

    # 0. instance reachable? print its version/pid so a stale binary is obvious.
    try:
        _, h = http("GET", f"{base}/health", timeout=3)
    except (urllib.error.URLError, ConnectionError) as e:
        fail(f"no TalosRed at {base} — start the binary first ({e})")
    try:
        info = json.loads(h)
        print(
            f"instance up at {base} (version={info.get('version')} "
            f"pid={info.get('pid')} uptime={info.get('uptime_seconds')}s)"
        )
    except (ValueError, AttributeError):
        print(
            f"instance up at {base} (legacy /health, no version — likely a stale binary)"
        )

    # local fake upstream
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), FakeUpstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    upstream_url = f"http://127.0.0.1:{upstream.server_address[1]}"

    auth = {"Authorization": f"Bearer {args.proxy_key}"}

    try:
        # 1. point the alias at the fake upstream (so no real keys are needed)
        try:
            http(
                "POST",
                f"{base}/ui/aliases",
                data=(
                    f"pattern={ALIAS}&target_model={TARGET_MODEL}"
                    f"&target_url={upstream_url}&provider=openai"
                ),
                form=True,
            )
        except urllib.error.HTTPError as e:
            fail(f"could not create alias (auth?): {e.code}")
        print(f"alias {ALIAS} -> {upstream_url}")

        # 1b. add pricing for the target model so cost calculations are non-zero.
        #     Cost is computed on the rewritten model name, so price TARGET_MODEL.
        try:
            http(
                "POST",
                f"{base}/ui/pricing",
                data=(
                    f"model={TARGET_MODEL}&provider=openai"
                    f"&input_per_1k=0.0010&output_per_1k=0.0020"
                ),
                form=True,
            )
        except urllib.error.HTTPError as e:
            fail(f"could not create pricing: {e.code}")
        print(f"pricing set for {TARGET_MODEL} ($0.0010/$0.0020 per 1k)")

        # 2. generate traffic — build the request queue, then consume it with a
        #    pool of `workers` concurrent senders.
        request_queue = []
        for _ in range(args.count):
            i = int(random.uniform(0, len(TAGS) - 1))
            app, user = TAGS[i]
            request_queue.append(
                (
                    {
                        "model": ALIAS,
                        "messages": [{"role": "user", "content": f"ping {i}"}],
                    },
                    app,
                    user,
                )
            )

        def send_one(item):
            payload, app, user = item
            try:
                status, _ = http(
                    "POST",
                    f"{base}/v1/chat/completions",
                    data=payload,
                    headers={**auth, "X-Talos-App": app, "X-Talos-User": user},
                )
            except urllib.error.HTTPError as e:
                hint = " — wrong --proxy-key?" if e.code == 401 else ""
                return f"request rejected ({e.code}){hint}"
            except (urllib.error.URLError, ConnectionError) as e:
                return f"request failed: {e}"
            if status != 200:
                return f"request returned {status}"
            return None

        sent = 0
        errors = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
            for err in pool.map(send_one, request_queue):
                if err:
                    errors.append(err)
                else:
                    sent += 1
        if errors:
            fail(
                f"{len(errors)}/{len(request_queue)} requests failed; first: {errors[0]}"
            )

        print(
            f"sent {sent} requests via {workers} workers "
            f"across apps {sorted({a for a, _ in TAGS})}"
        )

        expected = {
            app: sum(1 for i in range(args.count) if TAGS[i % len(TAGS)][0] == app)
            for app, _ in TAGS
        }

        # 3. confirm the requests show up in the logs.
        # NOTE: /ui/requests caps at 100 rows (list limit), so for large --count
        # we only assert that rows render — the exact totals are checked against
        # /ui/usage below, which aggregates with SQL COUNT and is not capped.
        _, html = http("GET", f"{base}/ui/requests?app=billing-service")
        ids = re.findall(r'data-id="(talos-[0-9a-f-]+)"', html)
        if not ids:
            fail("no billing-service rows rendered in /ui/requests")
        if ALIAS not in html and "llama-local" not in html:
            fail("logged rows missing the routed model")
        if "billing-service" not in html:
            fail("attribution app not rendered in logs")
        print(
            f"logs render billing-service rows (showing {len(ids)}, list caps at 100)"
        )

        # 4. /ui/usage aggregates ALL traffic by app — the uncapped source of truth.
        #    Logging is async (one goroutine + a single SQLite writer), so poll
        #    until the counts settle.
        deadline = time.time() + 30
        counts = {}
        while time.time() < deadline:
            _, usage = http("GET", f"{base}/ui/usage")
            counts = parse_usage_counts(usage)
            if all(counts.get(app, 0) >= expected[app] for app in expected):
                break
            time.sleep(0.25)

        for app, want in expected.items():
            got = counts.get(app, 0)
            if got < want:
                fail(f"usage shows {got} requests for {app!r}, expected >= {want}")
        print(
            "usage per-app request counts: "
            + ", ".join(f"{a}={counts[a]}" for a in sorted(expected))
        )

        # 5. cost calculation: with pricing set, at least one row must show $ > 0.
        cost_cells = [float(c) for c in re.findall(r'class="cost">\$([0-9.]+)', usage)]
        max_cost = max(cost_cells, default=0.0)
        if max_cost <= 0:
            fail("usage shows no non-zero cost — pricing not applied to the model")
        print(f"usage shows cost (max per-group ${max_cost:.6f})")

        print("\nPASS: generated traffic is visible in logs + usage (with cost)")
        print(f"      open {base}/ui to see it")
    finally:
        # tidy up the alias + pricing we created
        for path in (f"/ui/aliases/{ALIAS}", f"/ui/pricing/{TARGET_MODEL}"):
            try:
                http("DELETE", f"{base}{path}")
            except Exception:
                pass
        upstream.shutdown()


if __name__ == "__main__":
    main()
