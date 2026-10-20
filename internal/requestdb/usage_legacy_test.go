// Frozen pre-refactor report implementation: independent regression oracle.
package requestdb

import (
	"cmp"
	"context"
	"database/sql"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"maps"
	"slices"
	"strings"
	"time"
)

func (store *Store) legacyUsage(ctx context.Context, query api.UsageQuery) (api.UsageReport, error) {
	location := query.Location
	if location == nil {
		location = time.UTC
	}
	now := store.usageNow()
	from, to := query.From.UnixMilli(), query.To.UnixMilli()
	starts := bucketStarts(query.From, query.To, query.Bucket, location)
	totals := &aggregate{}
	buckets := make([]*aggregate, len(starts))
	for index := range buckets {
		buckets[index] = &aggregate{}
	}
	groups := map[string]map[string]*aggregate{}
	for _, dimension := range usageDimensions {
		groups[dimension] = map[string]*aggregate{}
	}
	pairs := map[[2]string]*aggregate{}
	stack := map[string][][2]int64{}
	add := func(target map[string]*aggregate, key string, value *entry) {
		if target[key] == nil {
			target[key] = &aggregate{}
		}
		target[key].add(value)
	}
	visit := func(value *entry) {
		index := bucketIndex(starts, value.time)
		totals.add(value)
		buckets[index].add(value)
		for _, dimension := range usageDimensions {
			add(groups[dimension], value.dimension(dimension), value)
		}
		pair := [2]string{value.account, value.model}
		if pairs[pair] == nil {
			pairs[pair] = &aggregate{}
		}
		pairs[pair].add(value)
		key := value.dimension(query.Stack)
		if stack[key] == nil {
			stack[key] = make([][2]int64, len(starts))
		}
		stack[key][index][0] += value.requests
		stack[key][index][1] += value.totalTokens
	}
	segments := plan(from, to, starts)
	for _, part := range segments {
		if err := store.scan(ctx, part, query.Filters, visit); err != nil {
			return api.UsageReport{}, err
		}
	}
	previous := &aggregate{}
	for _, part := range plan(from-(to-from), from, nil) {
		if err := store.scan(ctx, part, query.Filters, previous.add); err != nil {
			return api.UsageReport{}, err
		}
	}
	recent := &aggregate{}
	if err := store.scan(ctx, segment{from: now.Add(-recentWindow).UnixMilli(), to: now.UnixMilli() + 1}, query.Filters, recent.add); err != nil {
		return api.UsageReport{}, err
	}
	current, err := store.scanCost(ctx, from, to, starts, query.Filters)
	if err != nil {
		return api.UsageReport{}, err
	}
	before, err := store.scanCost(ctx, from-(to-from), from, nil, query.Filters)
	if err != nil {
		return api.UsageReport{}, err
	}
	unpriced := slices.AppendSeq([]string{}, maps.Keys(current.unpriced))
	slices.Sort(unpriced)
	report := api.UsageReport{
		From: query.From.UTC(), To: query.To.UTC(), BucketSeconds: int64(query.Bucket / time.Second), GeneratedAt: now.UTC(),
		Totals: totals.stats(), Previous: previous.stats(), Cost: current.total, PreviousCost: before.total, UnpricedModels: unpriced, StackBy: query.Stack,
		Recent:  api.UsageRecent{Minutes: int(recentWindow / time.Minute), Requests: recent.requests, Tokens: recent.totalTokens},
		Buckets: make([]api.UsageBucket, len(starts)), Groups: map[string][]api.UsageGroup{}, Pairs: []api.UsagePair{},
	}
	for index, start := range starts {
		report.Buckets[index] = api.UsageBucket{At: time.UnixMilli(start).UTC(), Cost: current.buckets[index], UsageStats: buckets[index].stats()}
	}
	for _, dimension := range usageDimensions {
		report.Groups[dimension] = rankGroups(groups[dimension])
	}
	for pair, total := range pairs {
		report.Pairs = append(report.Pairs, api.UsagePair{Account: pair[0], Model: pair[1], UsageStats: total.stats()})
	}
	slices.SortFunc(report.Pairs, func(a, b api.UsagePair) int {
		return cmp.Or(cmp.Compare(b.Requests, a.Requests), strings.Compare(a.Account, b.Account), strings.Compare(a.Model, b.Model))
	})
	report.Series = stackSeries(report.Groups[query.Stack], stack, len(starts))
	var latest sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT MAX(time) FROM requests`).Scan(&latest); err != nil {
		return api.UsageReport{}, err
	}
	if latest.Valid {
		at := time.UnixMilli(latest.Int64).UTC()
		report.LatestAt = &at
	}
	if clause, _ := filterSQL(query.Filters); clause != "" {
		options, err := store.options(ctx, segments)
		if err != nil {
			return api.UsageReport{}, err
		}
		report.Options = options
		return report, nil
	}
	report.Options = map[string][]string{}
	for _, dimension := range api.UsageDimensions {
		report.Options[dimension] = slices.Sorted(maps.Keys(groups[dimension]))
	}
	return report, nil
}
