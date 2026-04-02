#!/usr/bin/env python3
"""Generic webhook receiver for EasyCron playground.

When a webhook arrives with a `?check=<url>` query parameter, the receiver
GETs that URL and logs the result.  No hardcoded actions — the agent decides
what to check by encoding the target URL in the webhook URL it creates.

Convention (the agent knows this via its system prompt):
    webhook_url = http://host.docker.internal:9999/webhook?check=<url-encoded-target>

Examples:
    ?check=https://wttr.in/Montreal?format=j1
    ?check=https://www.githubstatus.com/api/v2/status.json

If no `check` param is present, the webhook is just logged.

    Terminal 1:  python playground/receiver.py
    Terminal 2:  python playground/agent.py
"""

import hashlib
import hmac
import json
import sys
import urllib.request
from collections import defaultdict
from datetime import datetime
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

WEBHOOK_SECRET = "playground-secret"
PORT = 9999

stats = {
    "total": 0,
    "by_job": defaultdict(int),
    "checks": {"ok": 0, "fail": 0},
    "signatures": {"valid": 0, "invalid": 0, "missing": 0},
}


def fetch(url: str, timeout: int = 10) -> tuple[int, dict | str]:
    """GET a URL.  Returns (status_code, parsed_json_or_text)."""
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "easycron-playground/1.0"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status = resp.status
            body = resp.read().decode("utf-8", errors="replace")
            try:
                return status, json.loads(body)
            except Exception:
                return status, body
    except urllib.error.HTTPError as exc:
        return exc.code, {"error": str(exc)}
    except Exception as exc:
        return 0, {"error": str(exc)}


def summarise(data) -> str:
    """Best-effort one-line summary of a check result."""
    if isinstance(data, dict):
        # Status page format (GitHub, Atlassian, Twitter, etc.)
        status = data.get("status", {})
        if isinstance(status, dict) and "description" in status:
            return status["description"]

        # wttr.in weather format
        cc = data.get("current_condition")
        if cc and isinstance(cc, list):
            c = cc[0]
            desc = c.get("weatherDesc", [{}])[0].get("value", "?")
            temp = c.get("temp_C", "?")
            feels = c.get("FeelsLikeC", "?")
            return f"{desc}, {temp}C (feels {feels}C)"

        # DNS resolver format (dns.google, Cloudflare DNS, etc.)
        if "Answer" in data and isinstance(data["Answer"], list):
            answers = data["Answer"]
            ips = [a["data"] for a in answers if "data" in a]
            name = answers[0].get("name", "?").rstrip(".") if answers else "?"
            return f"{name} -> {', '.join(ips[:4])}" + (f" (+{len(ips)-4} more)" if len(ips) > 4 else "")

        # Crypto price format (CoinGecko, etc.)
        if "bitcoin" in data or "ethereum" in data:
            parts = []
            for coin, prices in data.items():
                if isinstance(prices, dict):
                    for currency, val in prices.items():
                        parts.append(f"{coin}: ${val:,.2f}" if isinstance(val, (int, float)) else f"{coin}: {val} {currency}")
            if parts:
                return " | ".join(parts)

        # Error response
        if "error" in data and len(data) <= 2:
            return data["error"]

        # Generic: show top-level keys + first string/number value found
        preview = []
        for k, v in list(data.items())[:5]:
            if isinstance(v, (str, int, float, bool)):
                preview.append(f"{k}={v}")
        if preview:
            return ", ".join(preview)

        return f"{{{len(data)} keys}}"

    # Plain text — first meaningful line
    for line in str(data).split("\n"):
        line = line.strip()
        if line and not line.startswith("<"):  # skip HTML tags
            return line[:200]
    return "(empty or HTML-only response)"


class WebhookHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        # /dead — returns 400 immediately (non-retryable) for circuit breaker demos
        if self.path.startswith("/dead"):
            self.send_response(400)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"error":"simulated failure"}')
            now = datetime.now().strftime("%H:%M:%S")
            print(f"[{now}]  /dead  -> 400 (circuit breaker target)")
            return

        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length)

        stats["total"] += 1

        # Signature verification
        signature = self.headers.get("X-EasyCron-Signature", "")
        if signature:
            expected = hmac.new(WEBHOOK_SECRET.encode(), body, hashlib.sha256).hexdigest()
            valid = hmac.compare_digest(expected, signature)
            stats["signatures"]["valid" if valid else "invalid"] += 1
            sig_label = "OK" if valid else "INVALID"
        else:
            stats["signatures"]["missing"] += 1
            sig_label = "-"

        # Parse payload
        try:
            payload = json.loads(body)
        except Exception:
            payload = {}

        job_name = payload.get("job_name", payload.get("job_id", "?"))
        stats["by_job"][job_name] += 1

        now = datetime.now().strftime("%H:%M:%S")
        print(f"\n[{now}]  #{stats['total']}  {job_name}  (sig={sig_label})")

        # Extract check URL from query params
        parsed = urlparse(self.path)
        params = parse_qs(parsed.query)
        check_url = params.get("check", [None])[0]

        if check_url:
            status_code, data = fetch(check_url)
            summary = summarise(data)
            ok = 200 <= status_code < 400
            stats["checks"]["ok" if ok else "fail"] += 1
            icon = "OK" if ok else "FAIL"
            print(f"  [{icon} {status_code}] {check_url}")
            print(f"  -> {summary}")
        else:
            print(f"  (no ?check= param — logged only)")

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}')

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        out = {
            "total": stats["total"],
            "by_job": dict(stats["by_job"]),
            "checks": dict(stats["checks"]),
            "signatures": dict(stats["signatures"]),
        }
        self.wfile.write(json.dumps(out, indent=2).encode())

    def log_message(self, _fmt, *_args):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else PORT
    print(f"Webhook receiver listening on 0.0.0.0:{port}")
    print(f"  HMAC secret : {WEBHOOK_SECRET}")
    print(f"  Stats       : GET http://localhost:{port}/")
    print(f"  Webhook URL : http://host.docker.internal:{port}/webhook?check=<url>")
    print(f"\nWaiting for webhooks...\n")

    server = HTTPServer(("0.0.0.0", port), WebhookHandler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print(f"\n\nFinal: {stats['total']} webhooks | checks: {dict(stats['checks'])}")
