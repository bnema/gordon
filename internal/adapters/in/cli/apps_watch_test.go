package cli

// Tests for 202 deploy polling and `apps operations watch`: state-change
// progress, machine-readable JSON, terminal rendering/exit, bounded
// transient failures, fast 404, and Ctrl-C resume without server abort.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// shortenOperationPoll makes the polling cadence near-instant for tests.
func shortenOperationPoll(t *testing.T) {
	t.Helper()
	prev := operationPollInterval
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = prev })
}

func runningOp(op string, steps ...dto.AppStepDTO) *dto.AppDeployResponse {
	return &dto.AppDeployResponse{Op: op, App: "blog", Status: dto.AppStatusRunning, Steps: steps}
}

func TestRunAppDeploy_PollsAcceptedToTerminal(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(
		runningOp("op-1"), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "pending"}), nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppDeploy(context.Background(), plane, "blog", dto.AppDeployRequest{}, &out, &errOut, false))
	text := out.String()
	assert.Contains(t, text, "op op-1:")
	assert.Contains(t, text, "running")
	assert.Contains(t, text, "success")
}

func TestRunAppDeploy_UnchangedStateNotReprinted(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	step := dto.AppStepDTO{ID: "service.web.start", State: "pending"}
	unchanged := runningOp("op-1", step)
	advanced := runningOp("op-1",
		dto.AppStepDTO{ID: "service.web.start", State: "succeeded"},
		dto.AppStepDTO{ID: "service.web.health", State: "pending"})

	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1", step), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(unchanged, nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(advanced, nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppDeploy(context.Background(), plane, "blog", dto.AppDeployRequest{}, &out, &errOut, false))
	assert.Equal(t, 2, strings.Count(out.String(), "op op-1:"),
		"initial and one advanced poll render progress; the unchanged poll is not reprinted")
}

func TestRunAppDeploy_AcceptedIssuesNoSecondMutation(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	// .Once() on DeployApp proves the accepted path never re-POSTs; the
	// by-key lookups are the only way the outcome is observed.
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppDeploy(context.Background(), plane, "blog", dto.AppDeployRequest{}, &out, &errOut, false))
}

func TestRunAppDeploy_TerminalPartialRendersAndFails(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(&dto.AppDeployResponse{
		Op: "op-1", App: "blog", Status: "partial", Outcome: "partial",
		Services: map[string]dto.AppServiceResultDTO{
			"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "boom"},
		},
	}, nil).Once()

	var out, errOut bytes.Buffer
	err := runAppDeploy(context.Background(), plane, "blog", dto.AppDeployRequest{}, &out, &errOut, false)
	require.Error(t, err, "a terminal partial outcome must exit nonzero")
	assert.Contains(t, err.Error(), "partial")
	assert.Contains(t, out.String(), "boom", "the terminal journal is rendered before failing")
}

func TestRunAppDeploy_JSONKeepsStdoutMachineReadable(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "pending"}), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "succeeded"}), nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppDeploy(context.Background(), plane, "blog", dto.AppDeployRequest{}, &out, &errOut, true))

	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var got dto.AppDeployResponse
	require.NoError(t, dec.Decode(&got))
	assert.Equal(t, "op-1", got.Op)
	assert.Equal(t, "success", got.Outcome)
	var extra json.RawMessage
	assert.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout carries exactly one JSON document")
	assert.NotContains(t, out.String(), "op op-1:", "progress must not pollute JSON stdout")
	assert.Contains(t, errOut.String(), "op op-1:", "progress goes to stderr in JSON mode")
}

func TestWatchOperation_CancelPrintsResumeWithoutAbort(t *testing.T) {
	plane := appPlane(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out, errOut bytes.Buffer
	err := watchOperation(ctx, plane, "blog", "key-1", runningOp("op-1"), &out, &errOut, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	text := out.String()
	assert.Contains(t, text, "gordon apps operations watch blog --key key-1")
	assert.Contains(t, text, "not cancelled")
	assert.NotContains(t, text, "cancel op")
}

func TestWatchOperation_CancelKeepsJSONStdoutClean(t *testing.T) {
	plane := appPlane(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out, errOut bytes.Buffer
	err := watchOperation(ctx, plane, "blog", "key-1", runningOp("op-1"), &out, &errOut, true)
	require.Error(t, err)
	assert.Empty(t, out.String(), "JSON stdout stays empty on interruption")
	assert.Contains(t, errOut.String(), "gordon apps operations watch blog --key key-1")
}

func TestRunAppsOperationsWatch_ResumesExisting(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "pending"}), nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppsOperationsWatch(context.Background(), plane, &out, &errOut, "blog", "key-1", false))
	assert.Contains(t, out.String(), "success")
}

func TestAwaitOperation_TransientErrorsAreBounded(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	boom := errors.New("connection refused")
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(nil, boom).Times(operationPollMaxTransientErrors + 1)

	var progress bytes.Buffer
	_, err := awaitOperation(context.Background(), plane, "blog", "key-1", nil, &progress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gave up watching")
	assert.Contains(t, err.Error(), "gordon apps operations watch blog --key key-1")
	assert.Contains(t, progress.String(), "transient polling error")
}

func TestAwaitOperation_NotFoundFailsFast(t *testing.T) {
	plane := appPlane(t)
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "gone").Return(nil,
		&remote.HTTPError{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: "unknown operation"}).Once()

	_, err := awaitOperation(context.Background(), plane, "blog", "gone", nil, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer recorded")
}

func TestAppsOperationsWatchCmd_Flags(t *testing.T) {
	cmd := newAppsOperationsWatchCmd()
	assert.Equal(t, "watch APP", cmd.Use)
	assert.NotNil(t, cmd.Flags().Lookup("key"))
	assert.NotNil(t, cmd.Flags().Lookup("json"))

	subs := map[string]bool{}
	for _, sub := range newAppsOperationsCmd().Commands() {
		subs[sub.Name()] = true
	}
	assert.True(t, subs["show"])
	assert.True(t, subs["watch"])
}
