"""Recompute evidence from saved synthetic SSE and counters, without networking."""
import argparse
import json
from pathlib import Path

from long_context import digest, evaluate_output


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_dir",type=Path)
    args=parser.parse_args()
    def read(name):
        return json.loads((args.run_dir/name).read_text(encoding="utf-8-sig"))
    report=read("report.json")
    if report.get("stage")!="complete":
        raise SystemExit("Wait for the complete capture and both file counters")
    text, finish, errors = [], [], []
    events=0
    for line in (args.run_dir/"response.sse").read_text(encoding="utf-8").splitlines():
        if not line.startswith("data:"):
            continue
        event=json.loads(line[5:])
        events+=1
        if event.get("error"):
            errors.append(event["error"])
        for candidate in event.get("candidates",[]):
            parts=[p.get("text","") for p in candidate.get("content",{}).get("parts",[]) if not p.get("thought")]
            if finish and any(parts):
                errors.append({"message":"Text after terminal event"})
            text.extend(parts)
            if candidate.get("finishReason"):
                finish.append(candidate["finishReason"])
    output="".join(text)
    raw=(args.run_dir/"output.txt").read_bytes()
    assert output.encode()==raw and digest(raw)==report["output_sha256"]
    assert len(raw)==report["output_bytes"] and events==report["sse_events"]
    counts={}
    for label in ("input","output"):
        counter=read(label+"-count.json")
        content=(args.run_dir/(label+".txt")).read_bytes()
        assert counter["completed"] and counter["generation_request_sent"] is False and counter["generation_request_intercepted"] is True
        assert counter["file_sha256"]==digest(content) and counter["file_bytes"]==len(content)
        assert counter["token_count"]==report[label+"_tokens_file_counter"]
        counts[label]=counter["token_count"]
    request=read("request.json")
    assert request["contents"][0]["parts"][0]["text"].encode()==(args.run_dir/"input.txt").read_bytes()
    timing=[json.loads(line) for line in (args.run_dir/"event-timing.jsonl").read_text(encoding="utf-8").splitlines()]
    assert len(timing)==events
    text_times=[event["seconds"] for event in timing if event["text_chars"]]
    assert len(text_times)>1 and text_times[-1]-text_times[0]>0.1
    checks=evaluate_output(output,read("expected.json"))
    report.update(checks)
    report["saved_stream_reverified"]=True
    report["validation_note"]="Recomputed from saved SSE, exact request text and counter hashes; decoded CommonMark escapes for checkpoint comparison. No generation retry or additional counter request."
    report["passed"]=(counts["input"]>100000 and counts["output"]>3000 and finish==["STOP"] and not errors
        and all(checks["checkpoint_rows_exact"].values()) and checks["completion_sentinel_found"])
    (args.run_dir/"report.json").write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding="utf-8")
    print(json.dumps(report,ensure_ascii=False))
    return 0 if report["passed"] else 1


if __name__=="__main__":
    raise SystemExit(main())
