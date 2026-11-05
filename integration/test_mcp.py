"""Added: MCP contract tests against real, isolated OpenCode servers.

Every assertion is mandatory; failures exit nonzero. No skipped check is a pass.
"""
import base64
import contextlib
import json
import os
import re
import subprocess
import time
import urllib.error
import urllib.request

PASSWORD = "isolated-test-password"
BRIDGE_API_KEY = "isolated-bridge-api-key"
EXPECTED_TOOLS = {"opencode_" + name for name in (
    "setup", "ask", "reply", "run", "check", "conversation",
    "sessions_overview", "mcp_servers", "provider_test")}


def request(url, payload=None, method=None, headers=None, timeout=40):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Content-Type": "application/json", "Accept": "application/json, text/event-stream", **(headers or {})})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        raw = response.read()
        return (json.loads(raw) if raw else None), response.headers


def upstream(version, path, payload=None, method=None, password=PASSWORD):
    auth = base64.b64encode(("opencode:" + password).encode()).decode()
    return request("http://opencode-" + version + ":4096" + path, payload, method,
                   {"Authorization": "Basic " + auth})[0]


class MCP:
    def __init__(self, url, api_key=BRIDGE_API_KEY):
        self.url, self.counter, self.session = url, 0, None
        self.api_key = api_key
        result = self.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                         "clientInfo": {"name": "bridge-isolated-test", "version": "1"}})
        self.protocol = result["protocolVersion"]
        self.rpc("notifications/initialized", {}, notification=True)
        self.tools = {t["name"] for t in self.rpc("tools/list", {})["tools"]}
        assert self.tools == EXPECTED_TOOLS, self.tools

    def rpc(self, method, params, notification=False):
        self.counter += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            body["id"] = self.counter
        headers = {}
        if self.api_key:
            headers["Authorization"] = "Bearer " + self.api_key
        if hasattr(self, "protocol"):
            headers["MCP-Protocol-Version"] = self.protocol
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        result, response_headers = request(self.url, body, headers=headers)
        self.session = response_headers.get("Mcp-Session-Id", self.session)
        if notification:
            return
        assert "error" not in result, result
        return result["result"]

    def call(self, name, args=None, error=False):
        result = self.rpc("tools/call", {"name": "opencode_" + name, "arguments": args or {}})
        assert bool(result.get("isError")) == error, result
        text = "\n".join(p["text"] for p in result.get("content", []) if p["type"] == "text")
        if error:
            assert text.strip(), result
            return text
        return json.loads(text)


def ready(url):
    deadline, last = time.monotonic() + 120, None
    while time.monotonic() < deadline:
        try:
            mcp = MCP(url)
            mcp.call("setup")
            return mcp
        except (OSError, AssertionError, ValueError) as error:
            last = error
            time.sleep(0.5)
    raise AssertionError(f"Server not ready: {url}: {last}")


def walk(value):
    if isinstance(value, dict):
        yield value
        for item in value.values():
            yield from walk(item)
    if isinstance(value, list):
        for item in value:
            yield from walk(item)


def session_id(value):
    result = value["sessionId"]
    assert result.startswith("ses_"), result
    return result


def pending_case(mcp, marker, expected):
    error = mcp.call("ask", {"prompt": marker, "providerID": "mock", "modelID": "fixed", "agent": "build"}, error=True)
    assert expected in error, error
    match = re.search(r"ses_[A-Za-z0-9]+", error)
    assert match, error
    check = mcp.call("check", {"sessionId": match[0]})
    assert check["status"]["type"] == expected, check
    return match[0]


def contracts(version, exact_version):
    mcp = ready(f"http://bridge-{version}:8080/mcp")
    # Bridge credentials are separate from OpenCode's upstream Basic auth.
    for path, method, payload in (("/", "POST", {}), ("/mcp", "POST", {}), ("/mcp/sse", "GET", None), ("/opencode/setup", "GET", None), ("/opencode/ask", "POST", {"prompt": "MUST_NOT_BE_SENT"})):
        for authorization in (None, "Bearer incorrect"):
            headers = {} if authorization is None else {"Authorization": authorization}
            try:
                request(f"http://bridge-{version}:8080" + path, payload, method, headers)
                raise AssertionError("Bridge accepted missing or incorrect credentials")
            except urllib.error.HTTPError as error:
                assert error.code == 401 and error.headers.get("WWW-Authenticate"), error
    assert request(f"http://bridge-{version}:8080/healthz")[0]["status"] == "ok"
    setup = mcp.call("setup")
    assert setup["healthy"] and setup["health"]["version"] == exact_version, setup
    # HTTP authentication is checked against each actual OpenCode server.
    path = "/api/info" if version == "v2" else "/global/health"
    try:
        upstream(version, path, password="incorrect")
        raise AssertionError("OpenCode accepted invalid authentication")
    except urllib.error.HTTPError as error:
        assert error.code == 401, error
    assert upstream(version, path)["version"] == exact_version

    answer = mcp.call("ask", {"prompt": "BRIDGE_FIXED", "title": "isolated MCP contract",
                              "providerID": "mock", "modelID": "fixed", "agent": "build"})
    sid = session_id(answer)
    assert answer["message"].strip(), answer
    reply = mcp.call("reply", {"sessionId": sid, "prompt": "BRIDGE_FOLLOWUP"})
    assert session_id(reply) == sid and reply["message"].strip(), reply
    overview = mcp.call("sessions_overview")
    assert any(s.get("id") == sid for s in overview), overview
    conversation = mcp.call("conversation", {"sessionId": sid, "limit": 100})
    assert "BRIDGE_FOLLOWUP" in json.dumps(conversation), conversation
    assert len(conversation) >= 4, conversation
    check = mcp.call("check", {"sessionId": sid, "detailed": version == "v1"})
    assert check["session"]["id"] == sid and check["status"]["type"] == "idle", check
    if version == "v2":
        assert check["session"]["location"]["directory"] == "/work/a", check
        assert check["session"]["agent"] == "build", check
        assert check["session"]["model"]["providerID"] == "mock", check
        assert check["session"]["model"]["id"] == "fixed", check
        mcp.call("check", {"sessionId": sid, "detailed": True}, error=True)
    else:
        assert check["session"]["directory"] == "/work/a", check
        assert any(v.get("providerID") == "mock" and v.get("modelID") == "fixed" for v in walk(conversation)), conversation
        assert any(v.get("agent") == "build" for v in walk(conversation)), conversation
    run = mcp.call("run", {"prompt": "BRIDGE_RUN", "providerID": "mock", "modelID": "fixed", "agent": "build", "maxDurationSeconds": 30})
    assert run["status"] == "completed" and run["message"].strip(), run
    assert session_id(run) != sid, run
    mcp.call("mcp_servers")
    provider = mcp.call("provider_test", {"providerId": "mock", "modelID": "fixed"})
    assert provider["available"] and provider["message"].strip(), provider
    mcp.call("reply", {"sessionId": "ses_missing", "prompt": "BRIDGE_FIXED"}, error=True)

    # A real model tool call runs solely inside the test working volume.
    tool = mcp.call("ask", {"prompt": "BRIDGE_TOOL", "providerID": "mock", "modelID": "fixed", "agent": "build"})
    history = mcp.call("conversation", {"sessionId": session_id(tool), "limit": 100})
    completed = [v for v in walk(history) if v.get("type") == "tool" and v.get("state", {}).get("status") == "completed"]
    assert completed, history
    outputs = [v["state"].get("output", v["state"].get("content", [])) for v in completed]
    assert "bridge-tool" in json.dumps(outputs), outputs

    approval = pending_case(mcp, "BRIDGE_APPROVAL", "approval_required")
    question = pending_case(mcp, "BRIDGE_QUESTION", "question_required")
    print(f"PASS {version}: actual tool pending states: {approval}, {question}", flush=True)

    timed_out = mcp.call("run", {"prompt": "BRIDGE_TIMEOUT", "providerID": "mock", "modelID": "fixed", "maxDurationSeconds": 1}, error=True)
    assert "deadline" in timed_out.lower() or "timeout" in timed_out.lower(), timed_out
    # Scoped bridge must refuse a session created in another actual project.
    if version == "v2":
        foreign = upstream(version, "/api/session", {"title": "foreign", "location": {"directory": "/work/b"}})["data"]
        foreign_id = foreign["id"]
    else:
        foreign = upstream(version, "/session?directory=/work/b", {"title": "foreign"})
        foreign_id = foreign["id"]
    assert all(s["id"] != foreign_id for s in mcp.call("sessions_overview"))
    error = mcp.call("reply", {"sessionId": foreign_id, "prompt": "MUST_NOT_BE_SENT"}, error=True)
    assert "directory" in error, error
    foreign_path = ("/api/session/" if version == "v2" else "/session/") + foreign_id + "/message"
    foreign_history = upstream(version, foreign_path + ("" if version == "v2" else "?directory=/work/b"))
    assert "MUST_NOT_BE_SENT" not in json.dumps(foreign_history), foreign_history
    # Listing includes descendant directories across distinct Git projects;
    # similarly prefixed siblings remain outside the base directory tree.
    def create_at(directory):
        if version == "v2":
            return upstream(version, "/api/session", {"title": "listing scope", "location": {"directory": directory}})["data"]["id"]
        return upstream(version, "/session?directory=" + directory, {"title": "listing scope"})["id"]
    child_id = create_at("/work/a/nested")
    sibling_id = create_at("/work/a-other")
    listed_ids = {session["id"] for session in mcp.call("sessions_overview")}
    assert child_id in listed_ids and sibling_id not in listed_ids, listed_ids
    assert "directory" in mcp.call("reply", {"sessionId": child_id, "prompt": "MUST_NOT_BE_SENT"}, error=True)
    print(f"PASS {version}: nine MCP tools, auth, history, model/agent, async, tool execution, timeout, project ownership", flush=True)


@contextlib.contextmanager
def local_bridge(version, password=PASSWORD, base=None, api_key=BRIDGE_API_KEY):
    env = {"PATH": os.environ["PATH"], "BRIDGE_LISTEN_ADDRESS": "127.0.0.1:8099",
           "OPENCODE_BASE_URL": base or f"http://opencode-{version}:4096", "OPENCODE_API_VERSION": "auto",
           "OPENCODE_DIRECTORY": "/work/a", "OPENCODE_SERVER_USERNAME": "opencode",
           "OPENCODE_SERVER_PASSWORD": password, "BRIDGE_REQUEST_TIMEOUT": "2s",
           "OPENCODE_MCP_BRIDGE_API_KEY": api_key or ""}
    process = subprocess.Popen(["/bridge"], env=env)
    try:
        deadline = time.monotonic() + 10
        while True:
            try:
                yield_value = MCP("http://127.0.0.1:8099/mcp", api_key=api_key)
                break
            except OSError:
                if time.monotonic() > deadline:
                    raise
                time.sleep(0.1)
        yield yield_value
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


if __name__ == "__main__":
    for version, exact in (("v1", "1.2.27"), ("v2", "2.1.1")):
        contracts(version, exact)
        with local_bridge(version, password="incorrect") as client:
            assert "401" in client.call("setup", error=True)
        with local_bridge(version, base="http://127.0.0.1:1") as client:
            assert "cannot connect" in client.call("setup", error=True)
    with local_bridge("v2", api_key=None) as client:
        assert client.call("setup")["healthy"]
        assert request("http://127.0.0.1:8099/opencode/setup")[0]["healthy"]
    print("PASS all isolated contracts (mock LLM only; not real-model validation)", flush=True)
