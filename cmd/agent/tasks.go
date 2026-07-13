package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/MaramHarsha/CypherPanel/gen/agent/v1"
	"github.com/MaramHarsha/CypherPanel/internal/jobs"
	"github.com/MaramHarsha/CypherPanel/internal/paths"
	"github.com/MaramHarsha/CypherPanel/internal/platform"
)

// taskExecutor dispatches queued tasks to their handlers. Every handler must
// be idempotent — JetStream may deliver the same task more than once.
type taskExecutor struct {
	layout paths.Layout
	users  platform.SystemUsers
}

func (e *taskExecutor) Handle(ctx context.Context, t jobs.Task) error {
	switch t.Type {
	case jobs.TypeNoop:
		return nil

	case jobs.TypeSystemUserCreate:
		var p jobs.SystemUserCreatePayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
		}
		if p.Username == "" {
			return jobs.Permanent(errors.New("username is required"))
		}
		home := p.HomeDir
		if home == "" {
			home = e.layout.AccountHome(p.Username)
		}
		err := e.users.Create(ctx, p.Username, home)
		if errors.Is(err, platform.ErrUnsupported) {
			return jobs.Permanent(err)
		}
		return err

	default:
		// Unknown type: this agent build is older than the control plane.
		// Permanent-fail so it surfaces instead of retrying forever.
		return jobs.Permanent(fmt.Errorf("unknown task type %q (agent version %s)", t.Type, version))
	}
}

// reportResult delivers the outcome to CypherCore over gRPC, retrying
// briefly — a lost result would leave the task 'pending' forever.
func reportResult(client agentv1.AgentServiceClient, serverID string) jobs.ResultReporter {
	return func(ctx context.Context, t jobs.Task, taskErr error) {
		req := &agentv1.ReportTaskResultRequest{
			ServerId: serverID,
			TaskId:   t.ID,
			Status:   agentv1.TaskStatus_TASK_STATUS_SUCCEEDED,
		}
		if taskErr != nil {
			req.Status = agentv1.TaskStatus_TASK_STATUS_FAILED
			req.ErrorMessage = taskErr.Error()
		}

		for attempt := 1; attempt <= 3; attempt++ {
			rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := client.ReportTaskResult(rpcCtx, req)
			cancel()
			if err == nil || status.Code(err) == codes.NotFound {
				return
			}
			slog.Warn("reporting task result failed", "task_id", t.ID, "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		slog.Error("giving up reporting task result", "task_id", t.ID)
	}
}
