"""Local M2 demo: a signed receiver fails twice, then accepts the delivery."""

import argparse
import base64
import hashlib
import hmac
import json
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    parser.add_argument("--token", default="dev-admin-token-change-me")
    parser.add_argument("--port", type=int, default=18081)
    parser.add_argument("--target", default="http://host.docker.internal:18081/hook")
    args = parser.parse_args()
    attempts = []
    secret = b""

    class Receiver(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            body = self.rfile.read(int(self.headers["Content-Length"]))
            fields = dict(x.split("=", 1) for x in self.headers["X-EventRelay-Signature"].split(","))
            expected = hmac.new(secret, b"v1:" + fields["t"].encode() + b":" + body, hashlib.sha256).hexdigest()
            valid = hmac.compare_digest(fields["v1"], expected)
            attempts.append(int(self.headers["X-EventRelay-Attempt"]))
            self.send_response((500 if len(attempts) <= 2 else 204) if valid else 401)
            self.end_headers()

    def request(path, data=None, headers=None):
        req = urllib.request.Request(args.api + path, data=data, headers=headers or {})
        with urllib.request.urlopen(req, timeout=5) as response:
            return json.load(response)

    server = ThreadingHTTPServer(("0.0.0.0", args.port), Receiver)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        auth = {"Authorization": "Bearer " + args.token, "Content-Type": "application/json"}
        endpoint = request("/v1/endpoints", json.dumps({"url": args.target}).encode(), auth)
        secret = base64.b64decode(endpoint["delivery_secret"])
        ingest = base64.b64decode(endpoint["ingest_secret"])
        body = b'{"demo":"fails twice then succeeds"}'
        timestamp = str(int(time.time()))
        signature = hmac.new(ingest, b"v1:" + timestamp.encode() + b":" + body, hashlib.sha256).hexdigest()
        event = request("/v1/endpoints/" + endpoint["id"] + "/events", body,
                        {"Content-Type": "application/json", "X-EventRelay-Signature": "t=" + timestamp + ",v1=" + signature})
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            rows = request("/v1/events?endpoint_id=" + endpoint["id"], headers=auth)["events"]
            if rows and rows[0]["status"] == "delivered":
                assert attempts == [1, 2, 3], attempts
                print(json.dumps({"event_id": event["event_id"], "status": "delivered", "attempts": attempts}))
                return
            time.sleep(0.25)
        raise RuntimeError("delivery did not complete within 45 seconds")
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
