"""Example Crewbase plugin in Python: one `py_hello` tool."""
import os

from crewbase_plugin import Plugin

plugin = Plugin("hello-py", "0.1.0")


@plugin.tool(
    "py_hello",
    description="Greet someone from a Python plugin.",
    category="none",
    params={"type": "object", "properties": {"name": {"type": "string", "description": "Who to greet"}}},
)
def py_hello(args, ctx):
    return "Hello, %s! (from python, pid %d)" % (args.get("name") or "world", os.getpid())


if __name__ == "__main__":
    plugin.run()
