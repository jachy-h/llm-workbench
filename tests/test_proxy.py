"""不依赖 pytest 的冒烟测试: uv run python -m unittest discover -s tests -v"""

from __future__ import annotations

import json
import os
import sys
import threading
import unittest
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "src"))

from ollama_nothink_proxy import (
    Config,
    ProxyServer,
    chat_path_kind,
    inject_native,
    inject_v1,
    load_params,
)


class InjectTests(unittest.TestCase):
    def setUp(self):
        self.params = {"temperature": 0.2, "top_p": 0.9, "max_tokens": 96, "keep_alive": "30m"}

    def test_v1_forces_reasoning_effort_off(self):
        out = inject_v1({"model": "m", "messages": []}, self.params)
        self.assertEqual(out["reasoning_effort"], "none")
        self.assertEqual(out["max_tokens"], 96)

    def test_v1_drops_useless_think_field(self):
        out = inject_v1({"model": "m", "think": False}, self.params)
        self.assertNotIn("think", out)

    def test_client_values_win(self):
        out = inject_v1({"model": "m", "temperature": 0, "max_tokens": 8}, self.params)
        self.assertEqual(out["temperature"], 0)
        self.assertEqual(out["max_tokens"], 8)

    def test_native_maps_max_tokens_to_num_predict(self):
        out = inject_native({"model": "m"}, self.params)
        self.assertIs(out["think"], False)
        self.assertEqual(out["options"]["num_predict"], 96)
        self.assertEqual(out["options"]["temperature"], 0.2)
        self.assertEqual(out["keep_alive"], "30m")
        self.assertNotIn("reasoning_effort", out)

    def test_native_keeps_client_options(self):
        out = inject_native({"model": "m", "options": {"num_predict": 7}}, self.params)
        self.assertEqual(out["options"]["num_predict"], 7)

    def test_path_kind(self):
        self.assertEqual(chat_path_kind("/v1/chat/completions"), "v1")
        self.assertEqual(chat_path_kind("/api/chat"), "native")
        self.assertIsNone(chat_path_kind("/v1/models"))

    def test_param_priority(self):
        params = load_params('{"temperature": 0.5}', ["temperature=0", "seed=42"])
        self.assertEqual(params["temperature"], 0)
        self.assertEqual(params["seed"], 42)
        self.assertEqual(load_params(None, [], no_defaults=True), {})


class _Upstream(BaseHTTPRequestHandler):
    """假的 Ollama: 把收到的 body 原样回显。"""

    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n)
        data = json.dumps({"path": self.path, "got": json.loads(body or b"{}")}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        data = b'{"echo":"get"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class EndToEndTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.upstream = ThreadingHTTPServer(("127.0.0.1", 0), _Upstream)
        threading.Thread(target=cls.upstream.serve_forever, daemon=True).start()
        cfg = Config(
            upstream=f"http://127.0.0.1:{cls.upstream.server_address[1]}",
            params={"temperature": 0.2, "max_tokens": 96, "keep_alive": "30m"},
        )
        cls.proxy = ProxyServer(("127.0.0.1", 0), cfg)
        threading.Thread(target=cls.proxy.serve_forever, daemon=True).start()
        cls.base = f"http://127.0.0.1:{cls.proxy.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.proxy.shutdown()
        cls.proxy.server_close()
        cls.upstream.shutdown()
        cls.upstream.server_close()

    def _post(self, path, payload):
        req = urllib.request.Request(self.base + path, data=json.dumps(payload).encode(),
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())

    def test_chat_path_injected(self):
        out = self._post("/v1/chat/completions", {"model": "m", "temperature": 1})
        self.assertEqual(out["got"]["reasoning_effort"], "none")
        self.assertEqual(out["got"]["temperature"], 1)      # 客户端优先
        self.assertEqual(out["got"]["max_tokens"], 96)

    def test_non_chat_path_untouched(self):
        out = self._post("/api/show", {"model": "m", "temperature": 1})
        self.assertEqual(out["got"], {"model": "m", "temperature": 1})

    def test_get_passthrough(self):
        with urllib.request.urlopen(self.base + "/api/tags", timeout=10) as resp:
            self.assertEqual(json.loads(resp.read()), {"echo": "get"})


if __name__ == "__main__":
    unittest.main()
