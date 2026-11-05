"""Added: deterministic OpenAI chat fixture, with real OpenCode tool execution.

This does not mock OpenCode's API and never calls a paid provider.
"""
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"data": [{"id": "fixed", "object": "model"}]}).encode())

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404, "Only the chat completions contract is supported")
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = body.get("messages", [])
        user_index = max((i for i, m in enumerate(messages) if m["role"] == "user"), default=-1)
        prompt = json.dumps(messages[user_index].get("content", "")) if user_index >= 0 else ""
        tools = [t["function"] for t in body.get("tools", []) if t.get("type") == "function"]
        tool_result = any(m["role"] == "tool" for m in messages[user_index + 1:])
        if "BRIDGE_TIMEOUT" in prompt:
            time.sleep(8)
        if "BRIDGE_FAIL" in prompt:
            self.send_error(500, "Intentional fixture failure")
            return
        tool = None
        if not tool_result and any(s in prompt for s in ("BRIDGE_TOOL", "BRIDGE_APPROVAL", "BRIDGE_QUESTION")):
            if "BRIDGE_QUESTION" in prompt:
                tool = next((t for t in tools if t["name"] == "question"), None)
                arguments = {"questions": [{"header": "Fixture", "question": "Choose a test value", "options": [{"label": "A", "description": "Test A"}, {"label": "B", "description": "Test B"}]}]}
            else:
                tool = next((t for t in tools if t["name"] in ("bash", "shell")), None)
                command = "printf bridge-approval" if "BRIDGE_APPROVAL" in prompt else "printf bridge-tool > bridge-tool.txt; cat bridge-tool.txt"
                arguments = {"command": command, "timeout": 5000}
                if tool and tool["name"] == "bash":
                    arguments["description"] = "Isolated fixture command"
                if tool is None:
                    # V2 exposes most built-ins through its execute Code Mode
                    # tool (core/src/codemode/tool.ts in v2.1.1).
                    tool = next((t for t in tools if t["name"] == "execute"), None)
                    arguments = {"code": "return await tools.shell(" + json.dumps({"command": command, "timeout": 5000}) + ");"}
            if tool is None:
                # Fail visibly if an upstream version changes its tool surface.
                self.send_error(422, "Fixture cannot find requested tool; available=" + str([t["name"] for t in tools]))
                return
            tool = {"index": 0, "id": "call_bridge_fixture", "type": "function", "function": {"name": tool["name"], "arguments": json.dumps(arguments)}}
        usage = {"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14}
        if not body.get("stream"):
            message = {"role": "assistant", "content": None if tool else "bridge-ok"}
            if tool:
                message["tool_calls"] = [{k: v for k, v in tool.items() if k != "index"}]
            response = {"id": "chatcmpl-fixture", "object": "chat.completion", "created": 1, "model": "fixed", "choices": [{"index": 0, "message": message, "finish_reason": "tool_calls" if tool else "stop"}], "usage": usage}
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        def chunk(delta, finish=None, token_usage=None):
            result = {"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixed", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
            if token_usage:
                result["usage"] = token_usage
            self.wfile.write(("data: " + json.dumps(result) + "\n\n").encode())
            self.wfile.flush()
        try:
            chunk({"role": "assistant"})
            chunk({"tool_calls": [tool]} if tool else {"content": "bridge-ok"})
            chunk({}, "tool_calls" if tool else "stop", usage)
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass  # A timeout/cancellation test intentionally closes the stream.


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
