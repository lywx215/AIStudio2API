package app

import (
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

func TestBusinessUsageLogAndLedgerKeepLocalSource(t *testing.T) {
	entry := api.AccessLog{Status: 200, Method: "POST", Path: "/v1beta/models/business-test:generateContent", Model: "business-test", Channel: "business", Authorized: true, UpstreamAttempted: true, Usage: &aistudio.Usage{InputTokens: 20, OutputTokens: 5, ReasoningTokens: 3, TotalTokens: 28, Source: "local_gemma3_sentencepiece", Incomplete: true}}
	log := requestLogData(entry)
	row := requestRow(entry, time.Now())
	if log.Usage == nil || log.Usage.Source != entry.Usage.Source || !log.Usage.Incomplete || log.Usage.OutputTokens != 8 || row.TotalTokens != 28 || row.UsageSource != entry.Usage.Source || !row.UsageIncomplete {
		t.Fatal(log, row)
	}
}
