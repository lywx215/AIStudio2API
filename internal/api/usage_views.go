package api

import (
	"fmt"
	"slices"
	"time"
)

// ValidateView validates scoped queries without changing the legacy default.
func (query UsageQuery) ValidateView(hasKey bool) error {
	if !slices.Contains([]string{"", "all", "overview", "trends", "dimensions", "breakdown", "records-meta"}, query.View) {
		return fmt.Errorf("view 必须是 all、overview、trends、dimensions、breakdown 或 records-meta")
	}
	if query.View == "dimensions" {
		if !slices.Contains(append(slices.Clone(UsageDimensions), "status"), query.Dimension) || hasKey {
			return fmt.Errorf("dimensions 需要有效的 dimension，且不接受 key")
		}
	} else if query.View == "breakdown" {
		if !slices.Contains([]string{"account", "model"}, query.Dimension) || !hasKey {
			return fmt.Errorf("breakdown 需要 dimension=account 或 model 以及 key")
		}
	} else if query.Dimension != "" || hasKey {
		return fmt.Errorf("dimension 和 key 只用于 dimensions 或 breakdown")
	}
	return nil
}

// UsageMeta is shared by scoped responses; absent calculations have no wire fields.
type UsageMeta struct {
	From        time.Time           `json:"from"`
	To          time.Time           `json:"to"`
	GeneratedAt time.Time           `json:"generated_at"`
	LatestAt    *time.Time          `json:"latest_at,omitempty"`
	Options     map[string][]string `json:"options,omitempty"`
}

type UsageOverviewReport struct {
	UsageMeta
	BucketSeconds  int64         `json:"bucket_seconds"`
	Totals         UsageStats    `json:"totals"`
	Previous       UsageStats    `json:"previous"`
	Cost           float64       `json:"cost"`
	PreviousCost   float64       `json:"previous_cost"`
	UnpricedModels []string      `json:"unpriced_models"`
	Recent         UsageRecent   `json:"recent"`
	Buckets        []UsageBucket `json:"buckets"`
}

type UsageTrendBucket struct {
	At time.Time `json:"at"`
	UsageStats
}

type UsageTrendsReport struct {
	UsageMeta
	BucketSeconds int64              `json:"bucket_seconds"`
	Totals        UsageStats         `json:"totals"`
	Buckets       []UsageTrendBucket `json:"buckets"`
	StackBy       string             `json:"stack_by"`
	Series        []UsageSeries      `json:"series"`
}

type UsageDimensionsReport struct {
	UsageMeta
	Dimension string       `json:"dimension"`
	Requests  int64        `json:"requests"`
	Groups    []UsageGroup `json:"groups"`
}

type UsageBreakdownReport struct {
	UsageMeta
	Dimension string      `json:"dimension"`
	Key       string      `json:"key"`
	Pairs     []UsagePair `json:"pairs"`
}

type UsageRecordsMeta struct {
	UsageMeta
	Requests int64    `json:"requests"`
	Statuses []string `json:"statuses"`
}

// ForView preserves the full legacy shape and projects explicitly typed scoped DTOs.
func (report UsageReport) ForView(query UsageQuery) any {
	meta := UsageMeta{From: report.From, To: report.To, GeneratedAt: report.GeneratedAt, LatestAt: report.LatestAt, Options: report.Options}
	switch query.View {
	case "overview":
		return UsageOverviewReport{meta, report.BucketSeconds, report.Totals, report.Previous, report.Cost, report.PreviousCost, report.UnpricedModels, report.Recent, report.Buckets}
	case "trends":
		buckets := make([]UsageTrendBucket, len(report.Buckets))
		for index, bucket := range report.Buckets {
			buckets[index] = UsageTrendBucket{bucket.At, bucket.UsageStats}
		}
		return UsageTrendsReport{meta, report.BucketSeconds, report.Totals, buckets, report.StackBy, report.Series}
	case "dimensions":
		return UsageDimensionsReport{meta, query.Dimension, report.Totals.Requests, report.Groups[query.Dimension]}
	case "breakdown":
		return UsageBreakdownReport{meta, query.Dimension, query.Key, report.Pairs}
	case "records-meta":
		statuses := make([]string, 0, len(report.Groups["status"]))
		for _, group := range report.Groups["status"] {
			statuses = append(statuses, group.Key)
		}
		slices.Sort(statuses)
		return UsageRecordsMeta{meta, report.Totals.Requests, statuses}
	default:
		return report
	}
}
