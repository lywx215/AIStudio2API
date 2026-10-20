package requestdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// ManagementUsage reads only the safe ledger columns, with count and offset
// page in one read transaction. Historical account labels are mutable and are
// deliberately not interpreted as stable management identity.
func (store *Store) ManagementUsage(ctx context.Context, query contract.PageQuery) (contract.Page[contract.UsageRecord], error) {
	page := contract.Page[contract.UsageRecord]{Items: []contract.UsageRecord{}, Offset: query.Offset, Limit: query.Limit}
	if err := query.Validate(); err != nil {
		return page, err
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, time, model, status, state, duration_ms FROM requests ORDER BY time DESC, id DESC LIMIT ? OFFSET ?`, query.Limit, query.Offset)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var id, model, state string
		var at, duration int64
		var status int
		if err := rows.Scan(&id, &at, &model, &status, &state, &duration); err != nil {
			return page, errors.Join(err, rows.Close())
		}
		// Refuse corrupt/free-form identifiers rather than reflect unknown text.
		if !contract.ValidID(id) || duration < 0 || duration > contract.MaxSafeInteger {
			return page, errors.Join(fmt.Errorf("invalid ledger measurement"), rows.Close())
		}
		finished := time.UnixMilli(at).UTC()
		started := time.UnixMilli(at - duration).UTC()
		if finished.Year() < 1 || finished.Year() > 9999 || started.Year() < 1 || started.Year() > 9999 {
			return page, errors.Join(fmt.Errorf("invalid ledger timestamp"), rows.Close())
		}
		record := contract.UsageRecord{RequestID: id, DurationMS: duration, FinishedAt: contract.NewTimestamp(finished), StartedAt: contract.NewTimestamp(started), Outcome: "error"}
		if contract.ValidModel(model) {
			record.Model = &model
		}
		if status >= 100 && status <= 599 {
			record.StatusCode = &status
		}
		switch state {
		case "completed", "tool_calls", "limited", "blocked":
			record.Outcome = "success"
		case "cancelled":
			record.Outcome = "aborted"
		}
		page.Items = append(page.Items, record)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return page, err
	}
	if err := tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}
