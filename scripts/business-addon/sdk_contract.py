"""Exercise the pinned SDK against Go handlers through a socket-free pipe.

Run with the Python environment containing requirements-sdk.txt:
  python scripts/business-addon/sdk_contract.py

The Go test launches --bridge-worker with BUSINESS_SDK_PIPE=1. Each stdout
line is a JSON request {method,url,headers,body}; body is standard base64.
The corresponding stdin line is {status,headers,body}, with a base64 body.
An alternate local fixture can implement the same protocol and mount the full
host handler. This fixture mounts Gemini and the real Business file handlers;
it does not exercise host authentication/dispatch or a Google account.
"""

from __future__ import annotations

import base64
import importlib.metadata
import io
import json
import os
from pathlib import Path
import subprocess
import sys

SDK_VERSION = "2.32.0"
ORIGIN = "http://business-sdk.invalid"
MODEL = "business-test"
PROVIDER = {"X-AIStudio-Provider": "business"}


def main() -> int:
    actual = importlib.metadata.version("google-genai")
    if actual != SDK_VERSION:
        raise RuntimeError(f"google-genai=={SDK_VERSION} required, found {actual}")
    if "--bridge-worker" not in sys.argv:
        env = os.environ.copy()
        env["BUSINESS_GENAI_SDK_PYTHON"] = sys.executable
        return subprocess.call(
            ["go", "test", "-mod=readonly", "./internal/addons/business/gemini",
             "-run", "^TestGeminiSDKOfflineHarness$", "-count=1", "-v"],
            cwd=Path(__file__).resolve().parents[2], env=env,
        )
    if os.environ.get("BUSINESS_SDK_PIPE") != "1":
        raise RuntimeError("The worker requires its local Go pipe fixture")
    return worker()


def worker() -> int:
    import httpx
    from google import genai
    from google.genai import errors, types

    calls = 0
    last_generations = 0
    seen_inline = False

    def round_trip(request: httpx.Request) -> httpx.Response:
        nonlocal calls, last_generations, seen_inline
        # MockTransport is mandatory. No socket transport, proxy, credentials,
        # environment-derived endpoint, or user-supplied URL can reach a network.
        if request.url.scheme != "http" or request.url.host != "business-sdk.invalid" or request.url.port is not None:
            raise AssertionError(f"Request escaped the in-memory fixture: {request.url}")
        content = request.read()
        if request.url.path.endswith(":generateContent") and b"inlineData" in content:
            body = json.loads(content)
            blob = body["contents"][0]["parts"][0]["inlineData"]
            if blob.get("data") == "-_8=":
                assert blob.get("mime_type") == "image/png", blob
                seen_inline = True
        headers = {k: v for k, v in request.headers.items()
                   if k.lower() not in {"x-goog-api-key", "authorization"}}
        print(json.dumps({"method": request.method, "url": str(request.url),
                          "headers": headers, "body": base64.b64encode(content).decode("ascii")}), flush=True)
        line = sys.stdin.readline()
        if not line:
            raise RuntimeError("Go fixture closed its response pipe")
        response = json.loads(line)
        calls += 1
        last_generations = int(response["headers"]["X-Fixture-Generations"])
        return httpx.Response(response["status"], headers=response["headers"],
                              content=base64.b64decode(response["body"]), request=request)

    http = httpx.Client(transport=httpx.MockTransport(round_trip), trust_env=False)
    # SDK 2.32.0 replaces per-upload HttpOptions.headers while preparing a
    # resumable upload. The selector must therefore be configured on the client.
    options = types.HttpOptions(base_url=ORIGIN, api_version="v1beta", headers=PROVIDER,
                                httpx_client=http, retry_options=types.HttpRetryOptions(attempts=1))
    client = genai.Client(api_key="offline-inert-fixture", vertexai=False, http_options=options)

    def generate(contents="hello", config=None):
        response = client.models.generate_content(model=MODEL, contents=contents, config=config)
        assert response.text == "fixture-output", response
        assert response.candidates[0].finish_reason == types.FinishReason.STOP
        assert response.model_version is None and response.usage_metadata is None
        return response

    def rejected(contents="hello", config=None):
        before = last_generations
        try:
            client.models.generate_content(model=MODEL, contents=contents, config=config)
        except errors.ClientError as exc:
            assert exc.code == 400, exc
        else:
            raise AssertionError("Unsupported native request was accepted")
        assert before == last_generations, "Rejected request acquired an account"

    try:
        models = list(client.models.list(config=types.ListModelsConfig(
            page_size=1, http_options=types.HttpOptions(headers=PROVIDER))))
        assert len(models) == 2 and models[0].name == "models/" + MODEL
        model = client.models.get(model=MODEL)
        assert set(model.supported_actions) == {"generateContent", "streamGenerateContent"}
        generate()
        generate([types.Part.from_bytes(data=bytes([251, 255]), mime_type="image/png"), "read"])
        assert seen_inline, "Pinned SDK's URL-safe/snake-case wire shape was not exercised"
        for mime_type in ("text/plain", "application/pdf"):
            generate([types.Part.from_bytes(data=b"fixture bytes", mime_type=mime_type), "read"])
        generate(config=types.GenerateContentConfig(temperature=0,
            thinking_config=types.ThinkingConfig(include_thoughts=False, thinking_budget=0),
            response_json_schema={"type": "object", "properties": {
                "keep_snake_case": {"const": 9007199254740993}}}))
        response = generate("thought")
        thought = response.candidates[0].content.parts[0]
        assert thought.thought is True and thought.thought_signature == b"opaque-fixture"
        response = generate("suggestions")
        sideband = response.candidates[0].content.parts[0]
        assert sideband.text is None and sideband.inline_data.mime_type == "application/json+suggestions"
        assert sideband.inline_data.data == b'{"suggestions":["synthetic"],"enabled":false}'
        sideband_chunks = list(client.models.generate_content_stream(model=MODEL, contents="suggestions"))
        sideband = sideband_chunks[0].candidates[0].content.parts[0]
        assert sideband.text is None and sideband.inline_data.data == b'{"suggestions":["synthetic"],"enabled":false}'
        chunks = list(client.models.generate_content_stream(model=MODEL, contents="stream"))
        assert "".join(chunk.text or "" for chunk in chunks) == "fixture-output"
        assert chunks[-1].candidates[0].finish_reason == types.FinishReason.STOP
        response = http.post(ORIGIN + "/v1beta/models/" + MODEL + ":streamGenerateContent", json={
            "contents": [{"parts": [{"text": "json-array"}]}]})
        assert response.status_code == 200 and isinstance(response.json(), list)
        assert response.json()[-1]["candidates"][0]["finishReason"] == "STOP"
        rejected(config=types.GenerateContentConfig(system_instruction="unsupported system"))
        rejected(config=types.GenerateContentConfig(top_p=0))
        rejected(config=types.GenerateContentConfig(tools=[types.Tool(function_declarations=[
            types.FunctionDeclaration(name="unsupported_function", parameters={"type": "object"})])]))
        rejected(contents=[types.Content(role="user", parts=[types.Part(text="before")]),
                           types.Content(role="model", parts=[types.Part(text="history")]),
                           types.Content(role="user", parts=[types.Part(text="after")])])
        rejected(contents=types.Part.from_bytes(data=b"unsupported audio", mime_type="audio/wav"))

        uploaded = client.files.upload(file=io.BytesIO(b"fixture pdf bytes"),
            config=types.UploadFileConfig(mime_type="application/pdf", display_name="fixture.pdf",
                http_options=types.HttpOptions(headers=PROVIDER)))
        assert uploaded.name.startswith("files/biz-") and uploaded.mime_type == "application/pdf"
        assert uploaded.display_name == "fixture.pdf", "SDK snake_case display name was lost"
        assert uploaded.uri.startswith(ORIGIN + "/v1beta/files/biz-")
        fetched = client.files.get(name=uploaded.name)
        assert fetched.name == uploaded.name and fetched.sha256_hash == uploaded.sha256_hash
        assert fetched.display_name == "fixture.pdf" and fetched.size_bytes == len(b"fixture pdf bytes")
        assert len(list(client.files.list(config=types.ListFilesConfig(
            http_options=types.HttpOptions(headers=PROVIDER))))) == 1
        generate([types.Part.from_uri(file_uri=uploaded.uri, mime_type=uploaded.mime_type), "read"])
        download = http.get(uploaded.uri + ":download")
        assert download.status_code == 200 and download.content == b"fixture pdf bytes"
        before = last_generations
        rejected(contents=types.Part.from_uri(
            file_uri="https://external.invalid/v1beta/" + uploaded.name, mime_type="application/pdf"))
        assert last_generations == before
        client.files.delete(name=uploaded.name)
        try:
            client.files.get(name=uploaded.name)
        except errors.ClientError as exc:
            assert exc.code == 404
        else:
            raise AssertionError("Deleted file still exists")
    finally:
        client.close()
        http.close()
    print(json.dumps({"done": True, "sdk": SDK_VERSION, "requests": calls,
                      "generations": last_generations}), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
