"""crewbase_plugin: out-of-process plugins for Crewbase (stdlib only).

The host starts the plugin as a subprocess and speaks JSON-RPC 2.0 over
stdio, one JSON message per line (protocol version 1). stdout belongs to the
protocol: ``print`` goes to stderr once :meth:`Plugin.run` starts.

Example::

    from crewbase_plugin import Plugin

    plugin = Plugin("hello-py")

    @plugin.tool("py_hello", description="Say hello",
                 params={"type": "object", "properties": {"name": {"type": "string"}}})
    def hello(args, ctx):
        return "Hello, %s!" % args.get("name", "world")

    plugin.run()
"""

import json
import sys
import threading

PROTOCOL_VERSION = 1

__all__ = ["Plugin", "RpcError", "PROTOCOL_VERSION"]


class RpcError(Exception):
    """A JSON-RPC error (raised by host calls, or raised by handlers)."""

    def __init__(self, code, message, data=None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.data = data


class Host:
    """Host services. Every call is checked against the granted permissions."""

    def __init__(self, plugin):
        self._p = plugin

    def log(self, message, level="info"):
        self._p._notify("log", {"level": level, "message": str(message)})

    def notify(self, title, body="", kind="", urgent=False):
        return self._p.call("notify.send", {"title": title, "body": body, "kind": kind, "urgent": urgent})

    def resolve_credential(self, ref):
        return self._p.call("credentials.resolve", {"ref": ref})["value"]

    @property
    def permissions(self):
        return (self._p.init or {}).get("permissions") or {}

    @property
    def config(self):
        return (self._p.init or {}).get("config") or {}


class Plugin:
    def __init__(self, name="", version=""):
        self.name = name
        self.version = version
        self.init = None
        self.host = Host(self)
        self._tools = {}
        self._prompts = []
        self._channels = {}
        self._on_result = None
        self._pre = None
        self._post = None
        self._seq = 0
        self._pending = {}
        self._lock = threading.Lock()
        self._wlock = threading.Lock()
        self._out = None

    # ------------------------------------------------------------ definition

    def tool(self, name, description="", params=None, category="none", timeout_ms=0):
        """Decorator: register ``fn(args, ctx) -> str | dict``."""

        def deco(fn):
            self._tools[name] = {
                "fn": fn,
                "spec": {
                    "name": name,
                    "description": description,
                    "params": params or {"type": "object", "properties": {}},
                    "category": category,
                    "timeoutMs": timeout_ms,
                },
            }
            return fn

        return deco

    def prompt(self, name, text, order=0):
        self._prompts.append({"name": name, "text": text, "order": order})

    def on_tool_result(self, fn):
        """Decorator: observe finalized executions (tools/result)."""
        self._on_result = fn
        return fn

    def pre_execute(self, fn):
        """Decorator: ``fn(decision) -> None | {"verdict": "deny", "reason": ...} | {"summary": ...}``."""
        self._pre = fn
        return fn

    def post_execute(self, fn):
        """Decorator: ``fn(execution) -> None | {"content": ..., "isError": ...}``."""
        self._post = fn
        return fn

    def notify_channel(self, name):
        """Decorator: ``fn(notification)`` delivers a notification."""

        def deco(fn):
            self._channels[name] = fn
            return fn

        return deco

    # ------------------------------------------------------------ transport

    def _write(self, msg):
        msg["jsonrpc"] = "2.0"
        data = json.dumps(msg, separators=(",", ":")) + "\n"
        with self._wlock:
            self._out.write(data)
            self._out.flush()

    def _notify(self, method, params):
        self._write({"method": method, "params": params})

    def call(self, method, params):
        """Call a host method and wait for the result (raises RpcError)."""
        with self._lock:
            self._seq += 1
            mid = str(self._seq)
            ev = threading.Event()
            slot = {"ev": ev}
            self._pending[mid] = slot
        self._write({"id": mid, "method": method, "params": params})
        ev.wait()
        if "error" in slot:
            e = slot["error"] or {}
            raise RpcError(e.get("code", -32603), e.get("message", "error"), e.get("data"))
        return slot.get("result")

    def _registrations(self):
        events = []
        if self._on_result:
            events.append("tools/result")
        if self._pre:
            events.append("tools/pre-execute")
        if self._post:
            events.append("tools/post-execute")
        return {
            "protocolVersion": PROTOCOL_VERSION,
            "name": self.name,
            "version": self.version,
            "tools": [t["spec"] for t in self._tools.values()],
            "prompts": self._prompts,
            "events": events,
            "notify": [{"name": n} for n in self._channels],
        }

    def _handle(self, method, p):
        if method == "initialize":
            self.init = p
            return self._registrations()
        if method == "tool/execute":
            t = self._tools.get(p.get("tool"))
            if t is None:
                raise RpcError(-32601, "unknown tool %s" % p.get("tool"))
            call = p.get("call") or {}
            out = t["fn"](call.get("args") or {}, {"call": call, "host": self.host})
            if out is None:
                return {"content": ""}
            if isinstance(out, str):
                return {"content": out}
            return out
        if method == "tools/pre-execute":
            return self._pre(p) if self._pre else None
        if method == "tools/post-execute":
            return self._post(p) if self._post else None
        if method == "notify/send":
            ch = self._channels.get(p.get("channel"))
            if ch is None:
                raise RpcError(-32601, "unknown channel")
            ch(p.get("notification") or {})
            return None
        raise RpcError(-32601, "method not found: %s" % method)

    def _serve_request(self, msg):
        try:
            res = self._handle(msg["method"], msg.get("params") or {})
            self._write({"id": msg["id"], "result": res})
        except RpcError as e:
            self._write({"id": msg["id"], "error": {"code": e.code, "message": e.message, "data": e.data}})
        except Exception as e:  # noqa: BLE001 - report every failure to the host
            self._write({"id": msg["id"], "error": {"code": -32603, "message": "%s: %s" % (type(e).__name__, e)}})

    def _dispatch(self, msg):
        has_id = msg.get("id") is not None
        method = msg.get("method")
        if method and has_id:
            threading.Thread(target=self._serve_request, args=(msg,), daemon=True).start()
        elif method:
            if method == "tools/result" and self._on_result:
                try:
                    self._on_result(msg.get("params") or {})
                except Exception as e:  # noqa: BLE001
                    sys.stderr.write("on_tool_result failed: %s\n" % e)
            elif method == "shutdown":
                sys.exit(0)
        elif has_id:
            with self._lock:
                slot = self._pending.pop(str(msg["id"]), None)
            if slot is not None:
                if msg.get("error") is not None:
                    slot["error"] = msg["error"]
                else:
                    slot["result"] = msg.get("result")
                slot["ev"].set()

    def run(self):
        """Serve stdio until the host closes stdin."""
        self._out = sys.stdout
        sys.stdout = sys.stderr  # keep the protocol channel clean
        for line in sys.stdin:
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            self._dispatch(msg)
