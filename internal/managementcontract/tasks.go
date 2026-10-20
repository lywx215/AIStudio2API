package managementcontract

import (
	"context"
	"encoding/json"
	"fmt"
)

type TaskKind string

const (
	TaskImport     TaskKind = "import"
	TaskReplace    TaskKind = "replace"
	TaskTest       TaskKind = "test"
	TaskArchive    TaskKind = "archive"
	TaskRestore    TaskKind = "restore"
	TaskReload     TaskKind = "reload"
	TaskReloadAuth TaskKind = "reload-auth"
	TaskBatch      TaskKind = "batch"
)

type TaskState string

const (
	TaskQueued      TaskState = "queued"
	TaskRunning     TaskState = "running"
	TaskSucceeded   TaskState = "succeeded"
	TaskPartial     TaskState = "partial"
	TaskFailed      TaskState = "failed"
	TaskCancelled   TaskState = "cancelled"
	TaskInterrupted TaskState = "interrupted"
)

func (s TaskState) Terminal() bool {
	switch s {
	case TaskSucceeded, TaskPartial, TaskFailed, TaskCancelled, TaskInterrupted:
		return true
	}
	return false
}

type TaskCounts struct {
	Total       int `json:"total"`
	Queued      int `json:"queued"`
	Running     int `json:"running"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Cancelled   int `json:"cancelled"`
	Interrupted int `json:"interrupted"`
}
type TaskAccepted struct {
	TaskID string    `json:"taskId"`
	Status TaskState `json:"status"`
}
type TaskResult struct {
	AccountIDs []string `json:"accountIds,omitempty"`
	Changed    *bool    `json:"changed,omitempty"`
}

// ItemResult is a closed union; it never uses map[string]any for public data.
type ItemResult struct {
	Upload       *UploadOnlyResult
	Verification *VerificationResult
}

func (r ItemResult) MarshalJSON() ([]byte, error) {
	if (r.Upload == nil) == (r.Verification == nil) {
		return nil, fmt.Errorf("invalid management item result")
	}
	if r.Upload != nil {
		return json.Marshal(r.Upload)
	}
	return json.Marshal(r.Verification)
}
func (r *ItemResult) UnmarshalJSON(data []byte) error {
	var kind struct {
		Stage string `json:"stage"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	*r = ItemResult{}
	if kind.Stage == "uploaded" {
		r.Upload = &UploadOnlyResult{}
		return json.Unmarshal(data, r.Upload)
	}
	r.Verification = &VerificationResult{}
	return json.Unmarshal(data, r.Verification)
}

type TaskItem struct {
	ClientRef string         `json:"clientRef,omitempty"`
	AccountID string         `json:"accountId,omitempty"`
	Index     *int64         `json:"index,omitempty"`
	Status    TaskState      `json:"status"`
	Progress  int            `json:"progress"`
	Stage     string         `json:"stage"`
	Error     *Error         `json:"error,omitempty"`
	Result    *ItemResult    `json:"result,omitempty"`
	Upload    *UploadReceipt `json:"upload,omitempty"`
}
type Task struct {
	TaskID         string      `json:"taskId"`
	Kind           TaskKind    `json:"kind"`
	Status         TaskState   `json:"status"`
	CreatedByKeyID string      `json:"createdByKeyId"`
	CreatedAt      Timestamp   `json:"createdAt"`
	UpdatedAt      Timestamp   `json:"updatedAt"`
	StartedAt      *Timestamp  `json:"startedAt"`
	FinishedAt     *Timestamp  `json:"finishedAt"`
	Counts         TaskCounts  `json:"counts"`
	Items          []TaskItem  `json:"items"`
	Error          *Error      `json:"error,omitempty"`
	Result         *TaskResult `json:"result,omitempty"`
}

type TaskService interface {
	// Start is process-scoped and follows account recovery. It calls RecoverTasks
	// exactly once before workers start; never called again by StartService.
	// Close cooperatively stops work without undoing commits.
	Start(context.Context) error
	Submit(context.Context, Admission) (TaskAccepted, error)
	Cancel(context.Context, string, Principal) (Task, error)
	CancelForKey(context.Context, string) error
	Close(context.Context) error
}

type TaskServiceOptions struct {
	Store      Store
	Accounts   AccountCoordinator
	Verifier   Verifier
	Clock      Clock
	NewID      IDGenerator
	QueueLimit int
}
