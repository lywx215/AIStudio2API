package requestdb

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

func (store *Store) usageNow() time.Time {
	if store.now != nil {
		return store.now()
	}
	return time.Now()
}

// Usage keeps the original report available for existing clients.
func (store *Store) Usage(ctx context.Context, query api.UsageQuery) (api.UsageReport, error) {
	if err := query.ValidateView(query.View == "breakdown"); err != nil {
		return api.UsageReport{}, err
	}
	if query.View == "" || query.View == "all" {
		return store.usageAll(ctx, query)
	}
	return store.usageScoped(ctx, query)
}

func (store *Store) usageScoped(ctx context.Context, query api.UsageQuery) (api.UsageReport, error) {
	now := store.usageNow()
	report := api.UsageReport{From: query.From.UTC(), To: query.To.UTC(), GeneratedAt: now.UTC()}
	from, to := query.From.UnixMilli(), query.To.UnixMilli()
	overview := query.View == "overview"
	trends := query.View == "trends"
	var starts []int64
	if overview || trends {
		location := query.Location
		if location == nil {
			location = time.UTC
		}
		starts = bucketStarts(query.From, query.To, query.Bucket, location)
		report.BucketSeconds = int64(query.Bucket / time.Second)
	}
	segments := plan(from, to, starts)
	filters := query.Filters
	if query.View == "breakdown" {
		// Intersect with the parent; never replace a restrictive global filter.
		filters = maps.Clone(filters)
		if filters == nil {
			filters = api.UsageFilters{}
		}
		if values := filters[query.Dimension]; len(values) > 0 && !slices.Contains(values, query.Key) {
			report.Pairs = []api.UsagePair{}
			return report, nil
		}
		filters[query.Dimension] = []string{query.Key}
	}
	totals := &aggregate{}
	buckets := make([]*aggregate, len(starts))
	for index := range buckets {
		buckets[index] = &aggregate{}
	}
	groups := map[string]*aggregate{}
	pairs := map[[2]string]*aggregate{}
	stack := map[string][][2]int64{}
	clause, _ := filterSQL(filters)
	collectOptions := clause == "" && query.View != "records-meta" && query.View != "breakdown"
	seen := map[string]map[string]bool{}
	if collectOptions {
		for _, dimension := range api.UsageDimensions {
			seen[dimension] = map[string]bool{}
		}
	}
	if query.View == "records-meta" {
		// SQL counters do not read or decode either histogram column.
		for _, part := range segments {
			if err := store.statusCounts(ctx, part, filters, groups); err != nil {
				return report, err
			}
		}
		for _, group := range groups {
			totals.requests += group.requests
		}
		report.Groups = map[string][]api.UsageGroup{"status": rankGroups(groups)}
	} else {
		visit := func(value *entry) {
			if collectOptions {
				for _, dimension := range api.UsageDimensions {
					seen[dimension][value.dimension(dimension)] = true
				}
			}
			if overview || trends {
				totals.add(value)
				index := bucketIndex(starts, value.time)
				buckets[index].add(value)
				if trends {
					key := value.dimension(query.Stack)
					if groups[key] == nil {
						groups[key] = &aggregate{}
						stack[key] = make([][2]int64, len(starts))
					}
					groups[key].requests += value.requests
					stack[key][index][0] += value.requests
					stack[key][index][1] += value.totalTokens
				}
			} else {
				totals.requests += value.requests
				if query.View == "dimensions" {
					key := value.dimension(query.Dimension)
					if groups[key] == nil {
						groups[key] = &aggregate{}
					}
					groups[key].add(value)
				} else {
					key := [2]string{value.account, value.model}
					if pairs[key] == nil {
						pairs[key] = &aggregate{}
					}
					pairs[key].add(value)
				}
			}
		}
		for _, part := range segments {
			if err := store.scan(ctx, part, filters, visit); err != nil {
				return report, err
			}
		}
	}
	report.Totals = totals.stats()
	if overview {
		previous := &aggregate{}
		for _, part := range plan(from-(to-from), from, nil) {
			if err := store.scan(ctx, part, filters, previous.add); err != nil {
				return report, err
			}
		}
		report.Previous = previous.stats()
		// Recent throughput needs two counters, not latency distributions.
		clause, args := filterSQL(filters)
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(total_tokens),0) FROM requests WHERE time >= ? AND time < ?`+clause, append([]any{now.Add(-recentWindow).UnixMilli(), now.UnixMilli() + 1}, args...)...).Scan(&report.Recent.Requests, &report.Recent.Tokens); err != nil {
			return report, err
		}
		report.Recent.Minutes = int(recentWindow / time.Minute)
		current, err := store.scanCost(ctx, from, to, starts, filters)
		if err != nil {
			return report, err
		}
		before, err := store.scanCost(ctx, from-(to-from), from, nil, filters)
		if err != nil {
			return report, err
		}
		report.Cost, report.PreviousCost = current.total, before.total
		report.UnpricedModels = slices.AppendSeq([]string{}, maps.Keys(current.unpriced))
		slices.Sort(report.UnpricedModels)
		for index, start := range starts {
			report.Buckets = append(report.Buckets, api.UsageBucket{At: time.UnixMilli(start).UTC(), Cost: current.buckets[index], UsageStats: buckets[index].stats()})
		}
	} else if trends {
		report.StackBy = query.Stack
		report.Series = stackSeries(rankGroups(groups), stack, len(starts))
		for index, start := range starts {
			report.Buckets = append(report.Buckets, api.UsageBucket{At: time.UnixMilli(start).UTC(), UsageStats: buckets[index].stats()})
		}
	} else if query.View == "dimensions" {
		report.Groups = map[string][]api.UsageGroup{query.Dimension: rankGroups(groups)}
	} else if query.View == "breakdown" {
		report.Pairs = []api.UsagePair{}
		for pair, total := range pairs {
			report.Pairs = append(report.Pairs, api.UsagePair{Account: pair[0], Model: pair[1], UsageStats: total.stats()})
		}
		slices.SortFunc(report.Pairs, func(a, b api.UsagePair) int {
			return cmp.Or(cmp.Compare(b.Requests, a.Requests), strings.Compare(a.Account, b.Account), strings.Compare(a.Model, b.Model))
		})
		return report, nil
	}
	var latest sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT MAX(time) FROM requests`).Scan(&latest); err != nil {
		return report, err
	}
	if latest.Valid {
		at := time.UnixMilli(latest.Int64).UTC()
		report.LatestAt = &at
	}
	if collectOptions {
		report.Options = map[string][]string{}
		for dimension, values := range seen {
			report.Options[dimension] = slices.Sorted(maps.Keys(values))
		}
		return report, nil
	}
	options, err := store.options(ctx, segments)
	report.Options = options
	return report, err
}

func (store *Store) statusCounts(ctx context.Context, part segment, filters api.UsageFilters, target map[string]*aggregate) error {
	table, column := part.source.table()
	count := "SUM(requests)"
	if part.source == fromRequests {
		count = "COUNT(*)"
	}
	clause, args := filterSQL(filters)
	rows, err := store.db.QueryContext(ctx, `SELECT status, `+count+` FROM `+table+` WHERE `+column+` >= ? AND `+column+` < ?`+clause+` GROUP BY status`, append([]any{part.from, part.to}, args...)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var status int
		var requests int64
		if err := rows.Scan(&status, &requests); err != nil {
			return errors.Join(err, rows.Close())
		}
		key := strconv.Itoa(status)
		if target[key] == nil {
			target[key] = &aggregate{}
		}
		target[key].requests += requests
	}
	return errors.Join(rows.Err(), rows.Close())
}
