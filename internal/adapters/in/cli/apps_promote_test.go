package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// The promote command tests run in synctest bubbles: the watch loop sleeps
// on operationPollInterval, which the fake clock advances instantly once
// every goroutine is blocked. The control plane is an in-memory mock, so no
// real I/O crosses the bubble.

var promoteReq = dto.AppPromoteRequest{Service: "web", Image: "web@sha256:aaaa"}

func TestRunAppPromote_PollsAcceptedToTerminalAtTheInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plane := appPlane(t)
		plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(runningOp("key-1"), "key-1", nil).Once()
		start := time.Now()
		var polled []time.Duration
		plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").RunAndReturn(
			func(context.Context, string, string) (*dto.AppDeployResponse, error) {
				polled = append(polled, time.Since(start))
				if len(polled) < 2 {
					return runningOp("key-1", dto.AppStepDTO{ID: "service.web.replace", State: "pending"}), nil
				}
				return &dto.AppDeployResponse{Op: "key-1", App: "blog", Status: "success", Outcome: "success"}, nil
			}).Times(2)

		var out, errOut bytes.Buffer
		require.NoError(t, runAppPromote(context.Background(), plane, "blog", promoteReq, &out, &errOut, false))
		assert.Equal(t, []time.Duration{operationPollInterval, 2 * operationPollInterval}, polled,
			"polls follow the configured cadence")
		assert.Contains(t, out.String(), "key-1 blog: success")
	})
}

func TestRunAppPromote_JSONTerminalFailureExitsNonzero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plane := appPlane(t)
		plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(runningOp("key-1"), "key-1", nil).Once()
		plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
			&dto.AppDeployResponse{Op: "key-1", App: "blog", Status: "failed", Outcome: "failed"}, nil).Once()

		var out, errOut bytes.Buffer
		err := runAppPromote(context.Background(), plane, "blog", promoteReq, &out, &errOut, true)
		var failed *OperationFailedError
		require.ErrorAs(t, err, &failed)
		var doc dto.AppDeployResponse
		require.NoError(t, json.Unmarshal(out.Bytes(), &doc), "stdout holds exactly one JSON document")
		assert.Equal(t, "failed", doc.Outcome)
		assert.Contains(t, errOut.String(), "op key-1:", "progress goes to stderr in JSON mode")
	})
}

func TestRunAppPromote_InterruptedWatchPrintsResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plane := appPlane(t)
		plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(runningOp("key-1"), "key-1", nil).Once()
		plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(runningOp("key-1"), nil).Maybe()
		ctx, cancel := context.WithCancel(context.Background())
		var out, errOut bytes.Buffer
		var err error
		done := make(chan struct{})
		go func() {
			err = runAppPromote(ctx, plane, "blog", promoteReq, &out, &errOut, false)
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, out.String(), "gordon apps operations watch blog --key key-1")
	})
}

func TestRunAppPromote_TerminalReplayRendersWithoutPolling(t *testing.T) {
	plane := appPlane(t)
	plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(
		&dto.AppDeployResponse{Op: "key-1", App: "blog", Status: "success", Outcome: "success"}, "key-1", nil).Once()
	var out, errOut bytes.Buffer
	require.NoError(t, runAppPromote(context.Background(), plane, "blog", promoteReq, &out, &errOut, false))
	assert.Contains(t, out.String(), "success")
}

func TestRunAppPromote_ConflictIsNotRetried(t *testing.T) {
	t.Run("journaled conflict", func(t *testing.T) {
		plane := appPlane(t)
		conflict := &remote.AppOpConflictError{StatusCode: 409, Status: "409 Conflict",
			Response: dto.AppDeployResponse{Op: "key-1", App: "blog", Outcome: "failed"}}
		plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(&conflict.Response, "key-1", conflict).Once()
		var out, errOut bytes.Buffer
		err := runAppPromote(context.Background(), plane, "blog", promoteReq, &out, &errOut, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gordon apps operations show blog --key key-1")
		assert.Contains(t, out.String(), "key-1 blog: failed")
	})
	t.Run("state conflict", func(t *testing.T) {
		plane := appPlane(t)
		plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(nil, "key-1", errors.New("409 Conflict: state-conflict")).Once()
		var out, errOut bytes.Buffer
		err := runAppPromote(context.Background(), plane, "blog", promoteReq, &out, &errOut, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "state-conflict")
	})
	t.Run("ambiguous outcome", func(t *testing.T) {
		plane := appPlane(t)
		plane.EXPECT().PromoteApp(mock.Anything, "blog", promoteReq).Return(nil, "key-1",
			&remote.OutcomeUnknownError{Method: "POST", Path: "/apps/blog/promote", Err: errors.New("eof")}).Once()
		var out, errOut bytes.Buffer
		err := runAppPromote(context.Background(), plane, "blog", promoteReq, &out, &errOut, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outcome-unknown")
		assert.Contains(t, err.Error(), "--key key-1")
	})
}

func TestAppPromoteCmd_RequiresServiceAndImage(t *testing.T) {
	cmd := newAppPromoteCmd()
	cmd.SetArgs([]string{"blog", "--service", "web"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image")
}
