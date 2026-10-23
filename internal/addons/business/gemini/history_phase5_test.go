package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func TestPhase5HistoryNativeCodeRoundTripAndTampering(t *testing.T) {
	parts := []contract.Part{
		{Text: "pre"}, {Text: "fix"},
		{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(1)"}, Thought: true, ThoughtSignature: "opaque-code"},
		{CodeExecutionResult: &contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "1\n"}},
		{Text: "tail"},
	}
	wire, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(raw string) (contract.Request, error) {
		return Parse(strings.NewReader(historyBody(textTurn("user", "one"), `{"role":"model","parts":`+raw+`}`, textTurn("user", "two"))), binding())
	}
	parsed, err := parse(string(wire))
	if err != nil || len(parsed.History) != 3 || parsed.History[1] != contract.ModelHistoryDigest(parts) || len(parsed.QueryParts) != 1 || parsed.QueryParts[0]["text"] != "two" {
		t.Fatal("native output cannot continue without replay", err)
	}
	aliases := strings.NewReplacer("executableCode", "executable_code", "codeExecutionResult", "code_execution_result", "thoughtSignature", "thought_signature").Replace(string(wire))
	other, err := parse(aliases)
	if err != nil || other.History[1] != parsed.History[1] {
		t.Fatal("SDK aliases changed native history identity", err)
	}
	for _, changed := range []string{
		strings.Replace(string(wire), "print(1)", "print(2)", 1),
		strings.Replace(string(wire), "OUTCOME_OK", "OUTCOME_FAILED", 1),
		strings.Replace(string(wire), `"output":"1\n"`, `"output":"2\n"`, 1),
		strings.Replace(string(wire), "opaque-code", "opaque-other", 1),
	} {
		other, err := parse(changed)
		if err != nil || other.History[1] == parsed.History[1] {
			t.Fatal("native history tampering kept completed-prefix identity", err)
		}
	}
	for _, invalid := range []string{
		`[{"executableCode":{"language":"JAVASCRIPT","code":"x"}}]`,
		`[{"executableCode":{"language":"PYTHON","code":"x","id":"invented"}}]`,
		`[{"text":"","executableCode":{"language":"PYTHON","code":"x"}}]`,
		`[{"codeExecutionResult":{"outcome":"OUTCOME_UNSPECIFIED"}}]`,
		`[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":null}}]`,
		`[{"executableCode":{"language":"PYTHON","code":"x"},"executable_code":{"language":"PYTHON","code":"x"}}]`,
	} {
		if _, err := parse(invalid); err == nil {
			t.Fatal("malformed native history accepted", invalid)
		}
	}
	for _, part := range []string{`{"executableCode":{"language":"PYTHON","code":"x"}}`, `{"codeExecutionResult":{"outcome":"OUTCOME_OK"}}`} {
		if _, err := Parse(strings.NewReader(`{"contents":[{"role":"user","parts":[`+part+`]}]}`), binding()); err == nil {
			t.Fatal("native model output admitted as new user input")
		}
	}
}

func TestPhase5HistoryAdjacentModelFragmentsShareOneBoundedTurn(t *testing.T) {
	first, last := textTurn("user", "one"), textTurn("user", "two")
	fragments := []string{first, textTurn("model", "pre"), textTurn("model", "fix"), `{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]}`, `{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK"}}]}`, last}
	parsed, err := Parse(strings.NewReader(historyBody(fragments...)), binding())
	if err != nil || len(parsed.History) != 3 || parsed.History[1] != contract.ModelHistoryDigest([]contract.Part{{Text: "prefix"}, {ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(1)"}}, {CodeExecutionResult: &contract.CodeExecutionResult{Outcome: "OUTCOME_OK"}}}) {
		t.Fatal("adjacent SDK model fragments were split or reordered", parsed.History, err)
	}
	// Split across many raw model Contents so no per-fragment check can hide a
	// cumulative 4097th Part. The limit applies to the merged model turn.
	fragments = []string{first}
	for i := 0; i < 4096; i++ {
		fragments = append(fragments, textTurn("model", ""))
	}
	fragments = append(fragments, last)
	if _, err := Parse(strings.NewReader(historyBody(fragments...)), binding()); err != nil {
		t.Fatal("4096-part model turn rejected", err)
	}
	fragments = append(fragments[:len(fragments)-1], textTurn("model", ""), last)
	if _, err := Parse(strings.NewReader(historyBody(fragments...)), binding()); err == nil {
		t.Fatal("model fragments bypassed cumulative part limit")
	}
	turns := []string{first}
	for i := 0; i < 16; i++ {
		turns = append(turns, textTurn("model", "x"), textTurn("user", "y"))
	}
	if _, err := Parse(strings.NewReader(historyBody(turns...)), binding()); err != nil {
		t.Fatal("33-turn boundary rejected", err)
	}
	turns = append(turns, textTurn("model", "x"), textTurn("user", "y"))
	if _, err := Parse(strings.NewReader(historyBody(turns...)), binding()); err == nil {
		t.Fatal("logical history turn limit bypassed")
	}
}

func TestPhase5HistoryTypedStringsConsumeSharedBudgetAndRelease(t *testing.T) {
	for _, kind := range []string{"code", "result"} {
		t.Run(kind, func(t *testing.T) {
			payload := strings.Repeat("x", maxTextBytes)
			var part string
			if kind == "code" {
				part = `{"executableCode":{"language":"PYTHON","code":"` + payload + `"}}`
			} else {
				part = `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"` + payload + `"}}`
			}
			body := historyBody(textTurn("user", "one"), `{"role":"model","parts":[`+part+`]}`, textTurn("user", "two"))
			budget := resources.New(16 << 20)
			ctx, lease := budget.Begin(context.Background())
			_, err := ParseWithFiles(ctx, strings.NewReader(body), binding(), nil)
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" || failure.Code != 413 {
				t.Fatal("typed strings bypassed combined history text limit", err)
			}
			lease.Close()
			if budget.Snapshot().InUseBytes != 0 {
				t.Fatal("failed typed history retained budget after scope end")
			}
		})
	}
}
