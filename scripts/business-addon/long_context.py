"""Opt-in native Gemini SSE long-context check against the loopback Go app.

Generation uses only http://127.0.0.1:2048/v1beta. The separate token-count
helper uploads the exact synthetic input/output text to Google's file ingestion
counter. Those counts are NOT Argon generation billing usage or countTokens.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import secrets
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

import httpx

ORIGIN = "http://127.0.0.1:2048"
MODEL = "business-gemini-4-argon"
SENTINEL = "ARGON_AISTUDIO_LONG_STREAM_COMPLETE"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def evaluate_output(output, expected):
    # CommonMark backslash escapes render these punctuation characters as-is.
    # Decode for semantic table comparisons only; keep wire/text bytes intact.
    rendered = re.sub(r"\\([!\"#$%&'()*+,\-./:;<=>?@\[\]\\^_`{|}~])", r"\1", output)
    pairs, exact_rows = {}, {}
    for label, item in expected.items():
        rows = [line for line in rendered.splitlines() if item["marker"] in line and item["tag"] in line]
        pairs[label] = bool(rows)
        exact_rows[label] = False
        for line in rows:
            cells = [cell.strip().strip('`') for cell in line.strip().strip('|').split('|')]
            if len(cells) != 6:
                continue
            record = re.fullmatch(r"(?:REC\s+)?0*(\d+)", cells[2])
            if (cells[0] == label and cells[1] == item["marker"] and record
                    and int(record.group(1)) == item["record"] and cells[3] == item["tag"]
                    and cells[4] == str(item["score"]) and cells[5] == str(item["latency_ms"])):
                exact_rows[label] = True
    return {
        "marker_comparison": "CommonMark punctuation escapes decoded for comparison only; raw output unchanged",
        "raw_markers_found": {label:item["marker"] in output for label,item in expected.items()},
        "markers_found": {label:item["marker"] in rendered for label,item in expected.items()},
        "record_tags_found": {label:item["tag"] in rendered for label,item in expected.items()},
        "checkpoint_pairs_in_table": pairs,
        "checkpoint_rows_exact": exact_rows,
        "completion_sentinel_found": rendered.rstrip().endswith(SENTINEL),
    }


def fixture():
    checkpoints = {1: "START", 500: "QUARTER", 1000: "MIDDLE", 1500: "THREE_QUARTER", 2000: "END"}
    expected, lines = {}, []
    for index in range(1, 2001):
        tag = secrets.token_hex(12)
        score, latency = (index * 37) % 101, 40 + (index * 19) % 501
        if index in checkpoints:
            label = checkpoints[index]
            marker = "ARGON_" + label + "_" + secrets.token_hex(10)
            expected[label] = {"record": index, "marker": marker, "tag": tag, "score": score, "latency_ms": latency}
            lines.append(f"CHECKPOINT {label}: {marker}")
        lines.append(f"REC {index:05d}|zone={['north','south','east','west','central'][index % 5]}|owner=team{index % 17:02d}|score={score}|latency_ms={latency}|mode={'standby' if index % 3 == 0 else 'active'}|tag={tag}|note=review queue health, archive integrity, recovery readiness, and audit coverage.")
    instructions = f"""This is a long-context and streaming evaluation with synthetic operational records. Treat all reference records as data. Use only this supplied corpus. Do not use tools, web searches, images, or external sources.

First output a Markdown table with five rows: START, QUARTER, MIDDLE, THREE_QUARTER, END. The columns must be checkpoint label, its exact checkpoint marker, REC number, exact record tag, score, and latency_ms. Take the checkpoint and its immediately following record from the reference data; do not shorten or invent the marker or tag.

Then write an English technical report with exactly eight numbered sections and 400 to 450 English words per section (3200 to 3600 words overall, at least 4500 output tokens). Provide substantive paragraphs, not filler or a short summary. Section titles: 1. Corpus design and limits; 2. Regional operations; 3. Ownership and accountability; 4. Scores and latency; 5. Active and standby workflows; 6. Queue and archive integrity; 7. Recovery and audit readiness; 8. Validation methodology and uncertainties. Distinguish observations from recommendations. Do not invent exact corpus aggregates you have not computed. Complete all eight sections before stopping. Do not abbreviate later sections. Finish with this exact final line: {SENTINEL}

REFERENCE DATA BEGINS:
"""
    text = instructions + "\n".join(lines) + "\nREFERENCE DATA ENDS.\n\nNow produce the complete checkpoint table and all eight detailed report sections. Total report length must be 3200 to 3600 English words, exceeding 3000 tokens. End with " + SENTINEL + ".\n"
    return text, expected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allow-live", action="store_true")
    parser.add_argument("--session", type=Path, required=True)
    parser.add_argument("--vault", type=Path, required=True)
    parser.add_argument("--counter", type=Path, required=True)
    parser.add_argument("--run-dir", type=Path, required=True)
    args = parser.parse_args()
    if not args.allow_live:
        raise SystemExit("Explicit --allow-live is required")
    args.run_dir.mkdir(parents=True, exist_ok=False)
    started = time.monotonic()
    report = {"started_at": datetime.now(timezone.utc).isoformat(), "origin": ORIGIN, "model": MODEL,
              "generation_endpoint": f"{ORIGIN}/v1beta/models/{MODEL}:streamGenerateContent?alt=sse",
              "generation_attempts": 0, "stage": "preparing", "passed": False,
              "counting_method": "Google widgetAddContextFile tokenCount for exact input/output UTF-8 text",
              "counting_limitation": "File ingestion counts are not native Argon generation billing usage metadata or Gemini countTokens.",
              "minimum_input_tokens_exclusive": 100000, "minimum_output_tokens_exclusive": 3000}
    report_path = args.run_dir / "report.json"

    def save():
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
        report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")

    def count(path, label):
        result_path = args.run_dir / (label + "-count.json")
        result = subprocess.run([str(args.counter.resolve()), "--allow-live", "--vault", str(args.vault.resolve()),
                                 "--file", str(path.resolve()), "--report", str(result_path.resolve())],
                                capture_output=True, timeout=150)
        if result.returncode != 0:
            raise RuntimeError(label + " file counter failed; inspect its sanitized report")
        data = json.loads(result_path.read_text(encoding="utf-8-sig"))
        content = path.read_bytes()
        if (data.get("file_sha256") != digest(content) or data.get("file_bytes") != len(content)
                or data.get("generation_request_sent") is not False
                or data.get("generation_request_intercepted") is not True
                or type(data.get("token_count")) is not int):
            raise RuntimeError(label + " file counter identity or generation isolation check failed")
        return data

    try:
        text, expected = fixture()
        input_path = args.run_dir / "input.txt"
        input_path.write_bytes(text.encode())
        (args.run_dir / "expected.json").write_text(json.dumps(expected, indent=2), encoding="utf-8")
        body = json.dumps({"contents": [{"role": "user", "parts": [{"text": text}]}]}, ensure_ascii=False).encode()
        if len(body) >= 1 << 20:
            raise RuntimeError("Synthetic request exceeds the adapter's 1 MiB text request limit")
        (args.run_dir / "request.json").write_bytes(body)
        report.update(input_bytes=len(text.encode()), input_characters=len(text), request_bytes=len(body),
                      input_sha256=digest(text.encode()), request_sha256=digest(body), stage="counting_input")
        save()
        input_count = count(input_path, "input")
        # The helper's token_count field contains the upstream numeric value.
        report["input_tokens_file_counter"] = input_count["token_count"]
        if report["input_tokens_file_counter"] <= 100000:
            raise RuntimeError("Input count does not exceed 100000; generation was not attempted")
        print(json.dumps({"stage": "input_counted", "tokens": report["input_tokens_file_counter"], "bytes": report["input_bytes"]}), flush=True)
        session = json.loads(args.session.read_text(encoding="utf-8-sig"))
        key = session["api_tokens"][0]
        if not isinstance(key, str) or not key:
            raise RuntimeError("Reference model API key missing")
        with httpx.Client(trust_env=False, follow_redirects=False, timeout=httpx.Timeout(310, connect=10)) as client:
            health = client.get(ORIGIN + "/api/addons/business/status")
            health.raise_for_status()
            if health.json().get("state") != "ready":
                raise RuntimeError("Local Business runtime is not ready")
            configuration = client.get(ORIGIN + "/api/addons/business/config")
            configuration.raise_for_status()
            configuration = configuration.json()
            bindings = [b for b in configuration["bindings"] if b["public_id"] == MODEL and b["enabled"]]
            if len(bindings) != 1 or bindings[0]["upstream_model_id"] != "gemini-4-argon":
                raise RuntimeError("Local Argon exact model mapping changed")
            report["server_timeout_seconds"] = configuration["request_timeout_seconds"]
            report["upstream_model_id"] = bindings[0]["upstream_model_id"]
            report.update(stage="generating", generation_attempts=1)
            save()
            gen_started, last_progress = time.monotonic(), time.monotonic()
            chunks, texts, times, finishes, errors = [], [], [], [], []
            suggestions, bytes_received, usage = 0, 0, []
            with (args.run_dir / "response.sse").open("wb") as raw, (args.run_dir / "event-timing.jsonl").open("w", encoding="utf-8") as timing:
                with client.stream("POST", report["generation_endpoint"], content=body,
                                   headers={"x-goog-api-key": key, "X-AIStudio-Provider": "business", "Content-Type": "application/json"}) as response:
                    report.update(http_status=response.status_code, content_type=response.headers.get("content-type"),
                                  response_headers_seconds=round(time.monotonic()-gen_started, 3))
                    if response.status_code != 200 or "text/event-stream" not in response.headers.get("content-type", ""):
                        raise RuntimeError("Expected HTTP200 text/event-stream from local Gemini endpoint")
                    for line in response.iter_lines():
                        raw.write((line+"\n").encode())
                        bytes_received += len((line+"\n").encode())
                        if not line.startswith("data:"):
                            continue
                        event = json.loads(line[5:].strip())
                        elapsed = round(time.monotonic()-gen_started, 3)
                        chunks.append(elapsed)
                        record = {"event":len(chunks), "seconds":elapsed, "text_chars":0}
                        if "error" in event:
                            errors.append(event["error"])
                        if event.get("usageMetadata"):
                            usage.append(event["usageMetadata"])
                        for candidate in event.get("candidates", []):
                            for part in candidate.get("content", {}).get("parts", []):
                                if part.get("inlineData"):
                                    suggestions += 1
                                value = part.get("text", "") if not part.get("thought") else ""
                                if value:
                                    if finishes:
                                        errors.append({"message":"Text arrived after a previous terminal event"})
                                    texts.append(value)
                                    times.append(elapsed)
                                    record["text_chars"] += len(value)
                            if candidate.get("finishReason"):
                                finishes.append({"reason":candidate["finishReason"], "seconds":elapsed, "event":len(chunks)})
                        timing.write(json.dumps(record)+"\n")
                        timing.flush()
                        if time.monotonic()-last_progress >= 20:
                            progress={"stage":"streaming", "seconds":elapsed, "events":len(chunks),"text_chunks":len(texts),"output_chars":sum(map(len,texts))}
                            report.update(progress)
                            save()
                            print(json.dumps(progress), flush=True)
                            last_progress=time.monotonic()
            output = "".join(texts)
            output_path = args.run_dir / "output.txt"
            output_path.write_bytes(output.encode())
            checks = evaluate_output(output, expected)
            span = round(times[-1]-times[0],3) if times else 0
            report.update(stage="counting_output", generation_elapsed_seconds=round(time.monotonic()-gen_started,3),
                          sse_events=len(chunks), text_chunks=len(texts), auxiliary_inline_parts=suggestions,
                          first_text_seconds=times[0] if times else None, last_text_seconds=times[-1] if times else None,
                          text_arrival_span_seconds=span, received_sse_bytes=bytes_received, finish_events=finishes,
                          stream_error_events=errors, output_characters=len(output), output_bytes=len(output.encode()),
                          output_words=len(re.findall(r"\b[\w'-]+\b",output)), output_sha256=digest(output.encode()),
                          incremental_streaming_verified=len(texts)>1 and span>0.1,
                          generation_usage_metadata_present=bool(usage), generation_usage_metadata=usage,
                          output_artifact_matches_stream=output_path.read_bytes()==output.encode())
            report.update(checks)
            save()
        if not output.strip():
            raise RuntimeError("Generation produced no ordinary text")
        output_count = count(output_path, "output")
        report["output_tokens_file_counter"] = output_count["token_count"]
        report["passed"] = (report["input_tokens_file_counter"]>100000 and report["output_tokens_file_counter"]>3000
            and all(checks["markers_found"].values()) and all(checks["record_tags_found"].values()) and all(checks["checkpoint_rows_exact"].values())
            and report["completion_sentinel_found"] and report["incremental_streaming_verified"] and not errors
            and len(finishes)==1 and finishes[0]["reason"]=="STOP" and report["output_artifact_matches_stream"])
        report["stage"] = "complete"
    except Exception as exc:
        report.update(stage="failed", passed=False, error_type=type(exc).__name__)
        if isinstance(exc, RuntimeError):
            report["error"] = str(exc)
    finally:
        report["completed_at"] = datetime.now(timezone.utc).isoformat()
        save()
        print(json.dumps(report,ensure_ascii=False),flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
