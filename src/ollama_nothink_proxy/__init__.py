"""ollama-nothink-proxy — 给 Ollama 的 OpenAI 兼容层补上它不支持的参数。

背景
----
Ollama 的思考型模型（qwen3 / qwen3.5 …）默认开思考，而 OpenAI 兼容层
(``/v1/chat/completions``) 只认 ``reasoning_effort: "none"`` 这一个开关：
``think: false``、``chat_template_kwargs.enable_thinking`` 都会被静默忽略，
``num_ctx`` / ``num_predict`` / ``repeat_penalty`` 这类非标准字段同样被丢弃。

于是走 ``/v1`` 的客户端（例如 Easydict 的 Ollama 服务）每句话都要先思考：
短词 1s 起步、短句 2~5s，而且思考会吃掉 ``max_tokens`` 预算让正文变成空字符串；
请求不带 token 上限时还可能无限思考，把 runner 卡死。

这个代理做两件事，其余一律原样透传：

1. 强制关思考（``/v1`` 用 ``reasoning_effort: none``，原生 ``/api/chat`` 用 ``think: false``）；
2. 给缺失的生成参数填默认值（temperature / top_p / max_tokens / keep_alive）。

客户端显式传了的字段一定不会被改写，所以它和任意 UI 的参数设置共存。
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, Iterable, Mapping, Optional, Tuple

__version__ = "0.1.0"

DEFAULT_UPSTREAM = "http://127.0.0.1:11434"
DEFAULT_PORT = 11500

#: 默认注入值。只补客户端没给的字段，见 :func:`inject_v1` / :func:`inject_native`。
DEFAULT_PARAMS: Dict[str, Any] = {
    "temperature": 0.2,
    "top_p": 0.9,
    "max_tokens": 96,      # 也是防跑飞的硬上限
    "keep_alive": "30m",   # 方言: 避免每次冷加载 1.8s
}

V1_CHAT_PATHS = ("/v1/chat/completions",)
NATIVE_CHAT_PATHS = ("/api/chat",)

# 逐跳头 + 由本代理自己重新计算的头，不转发
_HOP_HEADERS = frozenset({
    "host", "content-length", "connection", "keep-alive", "proxy-authenticate",
    "proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade",
})


# --------------------------------------------------------------------------- #
# 参数注入
# --------------------------------------------------------------------------- #
def inject_v1(body: Mapping[str, Any], params: Mapping[str, Any]) -> Dict[str, Any]:
    """OpenAI 兼容层：补缺失参数并强制 ``reasoning_effort: none``。"""
    out: Dict[str, Any] = dict(body)
    for key, value in params.items():
        out.setdefault(key, value)
    out["reasoning_effort"] = "none"  # 开关，每次强制
    out.pop("think", None)            # 这个 path 上它只会被忽略，去掉免得误导
    return out


def inject_native(body: Mapping[str, Any], params: Mapping[str, Any]) -> Dict[str, Any]:
    """原生 ``/api/chat``：``think: false`` + 把参数塞进 ``options``。"""
    out: Dict[str, Any] = dict(body)
    options: Dict[str, Any] = dict(out.get("options") or {})
    for key, value in params.items():
        if key == "max_tokens":
            options.setdefault("num_predict", value)
        elif key in ("temperature", "top_p", "num_ctx", "repeat_penalty", "seed"):
            options.setdefault(key, value)
        else:  # keep_alive 等顶层字段
            out.setdefault(key, value)
    if options:
        out["options"] = options
    out["think"] = False
    out.pop("reasoning_effort", None)
    return out


def chat_path_kind(path: str) -> Optional[str]:
    """返回 ``"v1"`` / ``"native"``，不是 chat 路径则返回 ``None``。"""
    if path in V1_CHAT_PATHS:
        return "v1"
    if path in NATIVE_CHAT_PATHS:
        return "native"
    return None


# --------------------------------------------------------------------------- #
# 配置
# --------------------------------------------------------------------------- #
class Config:
    def __init__(
        self,
        upstream: str = DEFAULT_UPSTREAM,
        params: Optional[Mapping[str, Any]] = None,
        verbose: bool = False,
        timeout: float = 600.0,
    ) -> None:
        self.upstream = upstream.rstrip("/")
        if not self.upstream.startswith(("http://", "https://")):
            raise ValueError(f"upstream 只支持 http/https: {upstream!r}")
        self.params: Dict[str, Any] = dict(DEFAULT_PARAMS if params is None else params)
        self.verbose = verbose
        self.timeout = timeout

    def chat_paths(self) -> Iterable[str]:
        return V1_CHAT_PATHS + NATIVE_CHAT_PATHS


def coerce(value: str) -> Any:
    """把命令行 ``--set k=v`` 的字符串转成 JSON 能表达的类型。"""
    try:
        return json.loads(value)
    except (ValueError, TypeError):
        return value


def to_int(value: Any, default: int = 0) -> int:
    """宽容的 int 转换：拿不到数字就用默认值。"""
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def as_json_object(raw: str) -> Optional[Dict[str, Any]]:
    """宽松解析：拿不到 JSON 对象（语法错、类型不对）就返回 ``None``，不抛异常。"""
    return dict_only(coerce(raw))


def dict_only(value: Any) -> Optional[Dict[str, Any]]:
    """只放行字典，其余类型一律当解析失败。"""
    return value if isinstance(value, dict) else None


def load_params(
    raw_env: Optional[str] = None,
    overrides: Iterable[str] = (),
    no_defaults: bool = False,
) -> Dict[str, Any]:
    """优先级：默认值 < 环境变量 PROXY_PARAMS < 命令行 --set。"""
    if no_defaults:
        params: Dict[str, Any] = {}
    elif raw_env is None:
        params = dict(DEFAULT_PARAMS)
    else:
        parsed = as_json_object(raw_env or "{}")
        if parsed is None:
            raise ValueError(f"PROXY_PARAMS 需要 JSON 对象, 收到: {raw_env!r}")
        params = dict(parsed)
    for item in overrides:
        if "=" not in item:
            raise ValueError(f"--set 需要 key=value 形式，收到: {item!r}")
        key, _, value = item.partition("=")
        params[key.strip()] = coerce(value.strip())
    return params


# --------------------------------------------------------------------------- #
# HTTP 代理
# --------------------------------------------------------------------------- #
class ProxyHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = f"ollama-nothink-proxy/{__version__}"
    sys_version = ""

    @property
    def config(self) -> Config:
        return self.server.config  # type: ignore[attr-defined]

    def log_message(self, fmt: str, *args: Any) -> None:
        if self.config.verbose:
            sys.stderr.write(f"{self.log_date_time_string()} {fmt % args}\n")

    # --- 各 method 统一走 _handle -------------------------------------------
    def do_GET(self) -> None:
        self._handle("GET")

    def do_HEAD(self) -> None:
        self._handle("HEAD")

    def do_POST(self) -> None:
        self._handle("POST")

    def do_PUT(self) -> None:
        self._handle("PUT")

    def do_PATCH(self) -> None:
        self._handle("PATCH")

    def do_DELETE(self) -> None:
        self._handle("DELETE")

    # --- 核心 --------------------------------------------------------------
    def _read_body(self) -> bytes:
        length = to_int(self.headers.get("Content-Length"))
        return self.rfile.read(length) if length > 0 else b""

    def _rewrite(self, raw: bytes) -> Tuple[bytes, str]:
        """按路径注入参数，返回 (body, 说明)。"""
        kind = chat_path_kind(self.path.split("?", 1)[0])
        if kind is None or not raw:
            return raw, "pass"
        body = as_json_object(raw.decode("utf-8", "replace"))
        if body is None:
            return raw, "pass(非JSON对象)"
        params = self.config.params
        if kind == "v1":
            return json.dumps(inject_v1(body, params)).encode(), "injected:v1"
        return json.dumps(inject_native(body, params)).encode(), "injected:native"

    def _target_url(self) -> Optional[str]:
        """只允许把请求转给配置好的上游；非法路径返回 None。"""
        if not self.path.startswith("/") or self.path.startswith("//"):
            return None
        url = urllib.parse.urljoin(self.config.upstream, self.path)
        return url if url.startswith(self.config.upstream) else None

    def _forward(self, method: str, url: str, body: bytes) -> Any:
        req = urllib.request.Request(
            url,  # 已在 _target_url() 限定为配置的 http(s) 上游
            data=body if method not in ("GET", "HEAD") else None,
            method=method,
        )
        for key, value in self.headers.items():
            if key.lower() not in _HOP_HEADERS:
                req.add_header(key, value)
        if body:
            req.add_header("Content-Length", str(len(body)))
        return urllib.request.urlopen(req, timeout=self.config.timeout)

    def _handle(self, method: str) -> None:
        raw = self._read_body() if method not in ("GET", "HEAD") else b""
        body, note = self._rewrite(raw)

        url = self._target_url()
        if url is None:
            self._reply_json(400, {"error": {"message": "proxy: 非法路径"}})
            return

        try:
            resp = self._forward(method, url, body)
            status, headers = resp.status, resp.headers
        except urllib.error.HTTPError as exc:  # 上游 4xx/5xx 也照样回传
            resp, status, headers = exc, exc.code, exc.headers
        except Exception as exc:  # 连接失败等
            self.log_message("%s %s -> 502 (%s)", method, self.path, exc)
            self._reply_json(502, {"error": {"message": f"proxy: {exc}"}})
            return

        self.log_message("%s %s -> %s [%s]", method, self.path, status, note)

        try:
            self.send_response(status)
            for key, value in headers.items():
                if key.lower() not in _HOP_HEADERS:
                    self.send_header(key, value)
            if method == "HEAD" or status in (204, 304):
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            self._stream(resp)
        except (BrokenPipeError, ConnectionResetError):
            pass  # 客户端先走一步，正常
        finally:
            resp.close()

    def _stream(self, resp: Any) -> None:
        """分块转发。read1() 拿到多少发多少，保证首个 token 不被攒住。"""
        reader = getattr(resp, "read1", None) or resp.read
        while True:
            chunk = reader(8192)
            if not chunk:
                break
            self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))  # bytes 只能用 % 格式化
            self.wfile.flush()
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()

    def _reply_json(self, status: int, payload: Mapping[str, Any]) -> None:
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class ProxyServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address: Tuple[str, int], config: Config) -> None:
        super().__init__(address, ProxyHandler)
        self.config = config


# --------------------------------------------------------------------------- #
# CLI
# --------------------------------------------------------------------------- #
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="ollama-nothink-proxy",
        description="关掉 Ollama 思考并补默认生成参数的小代理（零第三方依赖）。",
        epilog=(
            "示例:\n"
            "  ollama-nothink-proxy\n"
            "  ollama-nothink-proxy --port 11434 --upstream http://127.0.0.1:11435\n"
            "  ollama-nothink-proxy --set temperature=0 --set max_tokens=128\n"
            "  PROXY_PARAMS='{}' ollama-nothink-proxy    # 只关思考, 不补参数"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--port", type=int, default=to_int(os.environ.get("PORT"), DEFAULT_PORT),
                        help=f"监听端口 (默认 {DEFAULT_PORT})")
    parser.add_argument("--host", default="127.0.0.1", help="监听地址 (默认 127.0.0.1)")
    parser.add_argument("--upstream", default=os.environ.get("UPSTREAM", DEFAULT_UPSTREAM),
                        help=f"Ollama 地址 (默认 {DEFAULT_UPSTREAM})")
    parser.add_argument("--set", dest="overrides", action="append", default=[],
                        metavar="KEY=VALUE", help="覆盖注入参数, 可重复")
    parser.add_argument("--no-defaults", action="store_true",
                        help="不注入任何参数, 只关思考")
    parser.add_argument("--timeout", type=float, default=600.0, help="上游超时秒数")
    parser.add_argument("-v", "--verbose", action="store_true", help="打印每个请求")
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    return parser


def main(argv: Optional[Iterable[str]] = None) -> int:
    args = build_parser().parse_args(list(argv) if argv is not None else None)

    try:
        params = load_params(os.environ.get("PROXY_PARAMS"), args.overrides, args.no_defaults)
    except (ValueError, json.JSONDecodeError) as exc:
        print(f"参数解析失败: {exc}", file=sys.stderr)
        return 2

    config = Config(upstream=args.upstream, params=params,
                    verbose=args.verbose, timeout=args.timeout)
    server = ProxyServer((args.host, args.port), config)

    shown = ", ".join(f"{k}={v!r}" for k, v in params.items()) or "(无)"
    print(f"ollama-nothink-proxy {__version__}")
    print(f"  listen : http://{args.host}:{args.port}")
    print(f"  upstream: {config.upstream}")
    print(f"  inject : {shown} + 强制关闭思考")
    print(f"  Easydict endpoint: http://{args.host}:{args.port}/v1/chat/completions")
    sys.stdout.flush()

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.shutdown()
        server.server_close()
        print("\nbye", file=sys.stderr)
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
