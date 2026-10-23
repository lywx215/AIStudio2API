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
             "-run", "^TestGeminiSDKCompletionMediaFilesAndCatalog/Python$" if "--completion-worker" in sys.argv else
             "^TestGeminiSDKPhase6RetrievedCitationsAndChat/Python$" if "--phase6-worker" in sys.argv else
             "^TestGeminiSDKPhase5PartsAndChat/Python$" if "--phase5-worker" in sys.argv else "^TestGeminiSDKOfflineHarness$", "-count=1", "-v"],
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
    last_wire_stream = None

    def round_trip(request: httpx.Request) -> httpx.Response:
        nonlocal calls, last_generations, seen_inline, last_wire_stream
        # MockTransport is mandatory. No socket transport, proxy, credentials,
        # environment-derived endpoint, or user-supplied URL can reach a network.
        if request.url.scheme != "http" or request.url.host != "business-sdk.invalid" or request.url.port is not None:
            raise AssertionError(f"Request escaped the in-memory fixture: {request.url}")
        content = request.read()
        if request.url.path.endswith(":generateContent") and b"inlineData" in content:
            body = json.loads(content)
            # A later model-history part may contain suggestions inlineData;
            # this probe concerns only the first user-uploaded image fixture.
            blob = body["contents"][0]["parts"][0].get("inlineData", {})
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
        response_bytes = base64.b64decode(response["body"])
        is_sse = any(key.lower() == "content-type" and value.startswith("text/event-stream")
                     for key, value in response["headers"].items())
        events = [json.loads(line[6:]) for line in response_bytes.decode("utf-8").splitlines()
                  if line.startswith("data: ")] if is_sse else []
        last_wire_stream = {"errors": [event["error"]["code"] for event in events if "error" in event],
                            "stops": sum(any(candidate.get("finishReason") == "STOP"
                                             for candidate in event.get("candidates", [])) for event in events)}
        return httpx.Response(response["status"], headers=response["headers"],
                              content=response_bytes, request=request)

    http = httpx.Client(transport=httpx.MockTransport(round_trip), trust_env=False)
    # SDK 2.32.0 replaces per-upload HttpOptions.headers while preparing a
    # resumable upload. The selector must therefore be configured on the client.
    options = types.HttpOptions(base_url=ORIGIN, api_version="v1beta", headers=PROVIDER,
                                httpx_client=http, retry_options=types.HttpRetryOptions(attempts=1))
    client = genai.Client(api_key="offline-inert-fixture", vertexai=False, http_options=options)

    if any(flag in sys.argv for flag in ("--phase5-worker", "--phase6-worker", "--completion-worker")):
        phase = "completion" if "--completion-worker" in sys.argv else 6 if "--phase6-worker" in sys.argv else 5
        try:
            run_phase = completion_worker if phase == "completion" else phase6_worker if phase == 6 else phase5_worker
            coverage = run_phase(client, types, errors, lambda: last_generations, lambda: last_wire_stream)
        finally:
            client.close()
            http.close()
        receipt = {"done": True, "phase": phase, "sdk": SDK_VERSION, "requests": calls,
                   "generations": last_generations, "coverage": coverage}
        if phase == 5:
            receipt["sdkStreamErrors"] = "raises_502"
        print(json.dumps(receipt), flush=True)
        return 0

    def generate(contents="hello", config=None):
        response = client.models.generate_content(model=MODEL, contents=contents, config=config)
        assert response.text == "fixture-output", response
        assert response.candidates[0].finish_reason == types.FinishReason.STOP
        assert response.model_version is None and response.usage_metadata is None
        return response

    def rejected(contents="hello", config=None, status=400):
        before = last_generations
        try:
            client.models.generate_content(model=MODEL, contents=contents, config=config)
        except errors.ClientError as exc:
            assert exc.code == status, exc
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
        # Optional fields without a Business mapping are accepted and ignored.
        generate(config=types.GenerateContentConfig(system_instruction="unsupported system"))
        generate(config=types.GenerateContentConfig(top_p=0))
        generate(config=types.GenerateContentConfig(safety_settings=[types.SafetySetting(
            category="HARM_CATEGORY_HARASSMENT", threshold="BLOCK_MEDIUM_AND_ABOVE")]))
        generate(config=types.GenerateContentConfig(tools=[types.Tool(function_declarations=[
            types.FunctionDeclaration(name="unsupported_function", parameters={"type": "object"})])]))
        rejected(contents=[types.Content(role="user", parts=[types.Part(text="before")]),
                           types.Content(role="model", parts=[types.Part(text="history")]),
                           types.Content(role="user", parts=[types.Part(text="after")])], status=409)
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


def phase5_worker(client, types, errors, generation_count, wire_stream):
    """Native parts and Chat behavior through the phase 5 real-handler fixture."""
    def expected_parts(outcome="OUTCOME_OK"):
        return [types.Part(text="雨A"), types.Part(executable_code=types.ExecutableCode(
            language="PYTHON", code="print(21 * 2)\n"), thought_signature=bytes([251, 255])),
            types.Part(code_execution_result=types.CodeExecutionResult(outcome=outcome, output="42\n")),
            types.Part(text="fin")]

    metadata = types.GroundingMetadata(
        grounding_chunks=[types.GroundingChunk(web=types.GroundingChunkWeb(
            uri="https://example.invalid/source", title="Synthetic source"))],
        grounding_supports=[types.GroundingSupport(segment=types.Segment(
            part_index=0, start_index=0, end_index=3, text="雨"), grounding_chunk_indices=[0])])
    def user(text):
        return types.Content(role="user", parts=[types.Part(text=text)])

    def check_response(response, outcome="OUTCOME_OK"):
        candidate = response.candidates[0]
        assert candidate.content.parts == expected_parts(outcome), candidate.content.parts
        assert candidate.grounding_metadata == metadata, candidate.grounding_metadata
        assert candidate.finish_reason == types.FinishReason.STOP
        assert response.usage_metadata is None and response.model_version is None

    def check_chunks(chunks):
        assert len(chunks) == 4, "Real parts must occur exactly once with no invented empty terminal"
        assert [part for chunk in chunks for part in chunk.candidates[0].content.parts] == expected_parts()
        assert chunks[-1].candidates[0].finish_reason == types.FinishReason.STOP
        assert chunks[-1].candidates[0].content.parts == [types.Part(text="fin")]
        assert chunks[-1].candidates[0].grounding_metadata == metadata
        assert all(chunk.candidates[0].grounding_metadata is None for chunk in chunks[:-1])
        assert wire_stream() == {"errors": [], "stops": 1}

    def followup(contents):
        before = generation_count()
        response = client.models.generate_content(model=MODEL, contents=contents)
        assert response.text == "phase5-followup-ok"
        assert response.candidates[0].finish_reason == types.FinishReason.STOP
        assert generation_count() == before + 1

    def rejected_history(contents):
        before = generation_count()
        try:
            client.models.generate_content(model=MODEL, contents=contents)
        except errors.ClientError as exc:
            assert exc.code == 409, exc
        else:
            raise AssertionError("Unrecognized model prefix was accepted")
        assert generation_count() == before, "Rejected prefix reached generation"

    response = client.models.generate_content(model=MODEL, contents="phase5-unary")
    check_response(response)
    followup([user("phase5-unary"), response.candidates[0].content, user("phase5-followup")])
    altered = response.candidates[0].content.model_copy(deep=True)
    altered.parts[1].executable_code.code = "print(0)\n"
    rejected_history([user("phase5-unary"), altered, user("phase5-followup")])
    for outcome in ("OUTCOME_FAILED", "OUTCOME_DEADLINE_EXCEEDED"):
        check_response(client.models.generate_content(model=MODEL, contents="phase5-outcome-" + outcome), outcome)
    chunks = list(client.models.generate_content_stream(model=MODEL, contents="phase5-stream"))
    check_chunks(chunks)
    followup([user("phase5-stream"), types.Content(role="model", parts=[
        part for chunk in chunks for part in chunk.candidates[0].content.parts]), user("phase5-followup")])

    chat = client.chats.create(model=MODEL)
    check_response(chat.send_message("phase5-chat-unary"))
    assert chat.send_message("phase5-followup").text == "phase5-followup-ok"
    streaming_chat = client.chats.create(model=MODEL)
    chat_chunks = list(streaming_chat.send_message_stream("phase5-chat-stream"))
    check_chunks(chat_chunks)
    history = streaming_chat.get_history(curated=True)
    assert history and history[0].role == "user", "Completed stream was excluded from curated history"
    assert len(history[1:]) > 1, "Pinned Python Chat consecutive model contents were not exercised"
    assert all(content.role == "model" for content in history[1:])
    assert [part for content in history[1:] for part in content.parts] == expected_parts()
    assert streaming_chat.send_message("phase5-followup").text == "phase5-followup-ok"

    failed_chat = client.chats.create(model=MODEL)
    partial = []
    before = generation_count()
    try:
        for chunk in failed_chat.send_message_stream("phase5-fail"):
            partial.append(chunk)
    except errors.ServerError as exc:
        assert exc.code == 502, exc
    else:
        raise AssertionError("Pinned Python SDK did not propagate the SSE error")
    assert generation_count() == before + 1, "Failed stream was retried"
    assert wire_stream() == {"errors": [502], "stops": 0}
    assert partial and all(chunk.candidates[0].finish_reason is None for chunk in partial)
    assert failed_chat.get_history(curated=True) == [], "Failed Chat entered curated history"
    rejected_history([user("phase5-fail"), types.Content(role="model", parts=[
        part for chunk in partial for part in chunk.candidates[0].content.parts]), user("phase5-followup")])
    return ["native-code-result-signature-and-exact-history", "execution-outcomes-are-successful-parts",
            "grounding-only-terminal-utf8-byte-indices", "native-chat-unary-stream-next-turn",
            "failed-chat-prefix-refused-without-retry"]


def phase6_worker(client, types, errors, generation_count, wire_stream):
    expected_parts = [types.Part(text="reason", thought=True, thought_signature=bytes([251, 255])),
        types.Part(text="雨A"), types.Part(executable_code=types.ExecutableCode(language="PYTHON", code="print(21 * 2)\n")),
        types.Part(code_execution_result=types.CodeExecutionResult(outcome="OUTCOME_OK", output="42\n")),
        types.Part(text="B雪"), types.Part(inline_data=types.Blob(mime_type="application/json+suggestions",
            data=b'{"suggestions":["synthetic"]}')), types.Part(text="尾")]
    grounding = types.GroundingMetadata(grounding_chunks=[
        types.GroundingChunk(retrieved_context=types.GroundingChunkRetrievedContext(uri="https://example.invalid/retrieved-one", title="Retrieved one")),
        types.GroundingChunk(retrieved_context=types.GroundingChunkRetrievedContext(uri="https://example.invalid/retrieved-two", title="Retrieved two"))],
        grounding_supports=[types.GroundingSupport(segment=types.Segment(part_index=part, start_index=start,
            end_index=end, text=text), grounding_chunk_indices=indices) for part, start, end, text, indices in [
            (1, 0, 3, "雨", [1]), (1, 3, 4, "A", [0, 1]), (4, 0, 4, "B雪", [0, 1]), (6, 0, 3, "尾", [1])]])
    # The wire key is citationSources; the SDK's public typed field is citations.
    citation = types.CitationMetadata(citations=[types.Citation(uri="https://example.invalid/citation", license="MIT")])
    def user(text):
        return types.Content(role="user", parts=[types.Part(text=text)])

    def check_metadata(candidate):
        assert candidate.grounding_metadata == grounding
        assert candidate.citation_metadata == citation
        assert all(chunk.web is None for chunk in candidate.grounding_metadata.grounding_chunks)
        assert all(source.start_index is None and source.end_index is None and source.title is None
                   and source.publication_date is None for source in candidate.citation_metadata.citations)

    def check_chunks(chunks):
        assert len(chunks) == len(expected_parts)
        assert [part for chunk in chunks for part in chunk.candidates[0].content.parts] == expected_parts
        assert chunks[-1].candidates[0].finish_reason == types.FinishReason.STOP
        assert chunks[-1].candidates[0].content.parts == [types.Part(text="尾")]
        check_metadata(chunks[-1].candidates[0])
        assert all(chunk.candidates[0].grounding_metadata is None and chunk.candidates[0].citation_metadata is None
                   for chunk in chunks[:-1])
        assert wire_stream() == {"errors": [], "stops": 1}

    def followup(contents):
        response = client.models.generate_content(model=MODEL, contents=contents)
        assert response.text == "phase6-followup-ok"
        assert response.candidates[0].finish_reason == types.FinishReason.STOP

    response = client.models.generate_content(model=MODEL, contents="phase6-unary")
    assert response.candidates[0].content.parts == expected_parts
    assert response.candidates[0].finish_reason == types.FinishReason.STOP
    assert response.usage_metadata is None
    check_metadata(response.candidates[0])
    followup([user("phase6-unary"), response.candidates[0].content, user("phase6-followup")])
    altered = response.candidates[0].content.model_copy(deep=True)
    altered.parts[2].executable_code.code = "print(0)\n"
    before = generation_count()
    try:
        client.models.generate_content(model=MODEL, contents=[user("phase6-unary"), altered, user("phase6-followup")])
    except errors.ClientError as exc:
        assert exc.code == 409, exc
    else:
        raise AssertionError("Mutated model code prefix was accepted")
    assert generation_count() == before
    chunks = list(client.models.generate_content_stream(model=MODEL, contents="phase6-stream"))
    check_chunks(chunks)
    followup([user("phase6-stream"), types.Content(role="model", parts=[
        part for chunk in chunks for part in chunk.candidates[0].content.parts]), user("phase6-followup")])
    chat = client.chats.create(model=MODEL)
    chat_chunks = list(chat.send_message_stream("phase6-chat-stream"))
    check_chunks(chat_chunks)
    history = chat.get_history(curated=True)
    assert history[0].role == "user" and len(history) == 1 + len(expected_parts)
    assert all(content.role == "model" for content in history[1:])
    assert [part for content in history[1:] for part in content.parts] == expected_parts
    assert chat.send_message("phase6-followup").text == "phase6-followup-ok"
    return ["retrieved-context-not-web-source-identity", "citationSources-sdk-citations-no-invented-offsets",
            "mixed-parts-utf8-split-supports-terminal-only", "chat-all-parts-continued-without-candidate-metadata"]


def completion_worker(client, types, errors, generation_count, wire_stream):
    names = ["models/business-test", "models/business-other", "models/business-third"]
    for page_size in (1, 1001):
        assert [item.name for item in client.models.list(config=types.ListModelsConfig(page_size=page_size))] == names
    for name in names:
        assert client.models.get(model=name).name == name
    try:
        list(client.models.list(config=types.ListModelsConfig(page_size=0)))
    except errors.ClientError as exc:
        assert exc.code == 400
    else:
        raise AssertionError("Zero model page size was accepted")
    uploads = []
    for index, suffix in enumerate(("one", "two", "three")):
        data = ("SDK fixture " + suffix).encode()
        uploaded = client.files.upload(file=io.BytesIO(data), config=types.UploadFileConfig(
            mime_type="text/plain", display_name=f"fixture {index + 1}.txt"))
        assert uploaded.source == types.FileSource.UPLOADED and uploaded.download_uri is None
        fetched = client.files.get(name=uploaded.name)
        assert fetched.sha256_hash == uploaded.sha256_hash and fetched.size_bytes == len(data)
        assert fetched.state == types.FileState.ACTIVE and fetched.expiration_time is not None
        uploads.append((uploaded, data))
    for page_size in (1, 1001):
        listed = list(client.files.list(config=types.ListFilesConfig(page_size=page_size)))
        assert len(listed) == len(uploads)
        assert {item.name for item in listed} == {item.name for item, _ in uploads}
    assert client.files.download(file=uploads[0][0].name) == uploads[0][1]
    # Uploaded File objects intentionally have no download_uri; the pinned
    # SDK rejects this overload locally. Do not relabel them as GENERATED.
    try:
        client.files.download(file=uploads[0][0])
    except ValueError:
        pass
    else:
        raise AssertionError("Pinned Python uploaded-File download restriction changed")
    file_response = client.models.generate_content(model=MODEL, contents=[
        *[types.Part.from_uri(file_uri=item.uri, mime_type=item.mime_type) for item, _ in uploads], "completion-files"])
    assert file_response.text == "completion-files-ok"
    for uploaded, _ in uploads:
        client.files.delete(name=uploaded.name)
        try:
            client.files.get(name=uploaded.name)
        except errors.ClientError as exc:
            assert exc.code == 404
        else:
            raise AssertionError("Deleted file still exists")

    data = bytes([251, 255, 0, 1, 10])
    expected = [types.Part(text="media-start"), types.Part(inline_data=types.Blob(mime_type="image/png", data=data),
        thought=True, thought_signature=bytes([251, 255])),
        *[types.Part(inline_data=types.Blob(mime_type="audio/pcm;rate=24000", data=data)) for _ in range(33)],
        types.Part(inline_data=types.Blob(mime_type="video/mp4", data=data)),
        types.Part(inline_data=types.Blob(mime_type="application/pdf", data=data)), types.Part(text="media-end")]
    def user(text):
        return types.Content(role="user", parts=[types.Part(text=text)])
    def followup(contents):
        response = client.models.generate_content(model=MODEL, contents=contents)
        assert response.text == "completion-followup-ok" and response.candidates[0].finish_reason == types.FinishReason.STOP
    first = client.models.generate_content(model=MODEL, contents="completion-media")
    assert first.candidates[0].content.parts == expected and first.candidates[0].finish_reason == types.FinishReason.STOP
    followup([user("completion-media"), first.candidates[0].content, user("completion-followup")])
    altered = first.candidates[0].content.model_copy(deep=True)
    altered.parts[1].inline_data.data = b"changed"
    before = generation_count()
    try:
        client.models.generate_content(model=MODEL, contents=[user("completion-media"), altered, user("completion-followup")])
    except errors.ClientError as exc:
        assert exc.code == 409
    else:
        raise AssertionError("Mutated media history was accepted")
    assert generation_count() == before
    def check_chunks(chunks):
        assert [part for chunk in chunks for part in chunk.candidates[0].content.parts] == expected
        assert chunks[-1].candidates[0].finish_reason == types.FinishReason.STOP
        assert chunks[-1].candidates[0].content.parts == [types.Part(text="media-end")]
        assert wire_stream() == {"errors": [], "stops": 1}
    chunks = list(client.models.generate_content_stream(model=MODEL, contents="completion-stream"))
    check_chunks(chunks)
    followup([user("completion-stream"), types.Content(role="model", parts=[
        part for chunk in chunks for part in chunk.candidates[0].content.parts]), user("completion-followup")])
    chat = client.chats.create(model=MODEL)
    chat_chunks = list(chat.send_message_stream("completion-chat"))
    check_chunks(chat_chunks)
    assert [part for content in chat.get_history(curated=True)[1:] for part in content.parts] == expected
    assert chat.send_message("completion-followup").text == "completion-followup-ok"
    before = generation_count()
    for call, status in ((lambda: client.models.generate_content(model=MODEL, contents="completion-invalid",
            config=types.GenerateContentConfig(temperature=-1)), 400),
            (lambda: client.models.count_tokens(model=MODEL, contents="completion-count"), 501)):
        try:
            call()
        except errors.APIError as exc:
            assert exc.code == status
        else:
            raise AssertionError("Unsupported or invalid request succeeded")
        assert generation_count() == before
    return ["models-pagination-clamp-1001-zero-rejected", "files-resumable-multiple-pages-and-uploaded-metadata",
            "sdk-download-by-name-reference-and-delete", "binary-media-mime-annotation-and-history-integrity",
            "binary-media-sse-and-native-chat-roundtrip", "mapped-invalid-and-count-unavailable-without-generation"]


if __name__ == "__main__":
    raise SystemExit(main())
