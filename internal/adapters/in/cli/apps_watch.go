package cli

// Operation watch: a 202 mutation (or an explicit resume) is observed
// through the existing by-key journal endpoint until it reaches a terminal
// state. Polling is intentionally local: the CLI never reissues the
// mutation and never asks the daemon to cancel the operation, so Ctrl-C
// only stops this process from watching.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// operationPollInterval is the pause between by-key polls of a running
// operation. It is a package variable (no flag) so command tests can
// shorten it; production uses a modest one-second cadence.
var operationPollInterval = time.Second

// operationPollMaxTransientErrors bounds consecutive transient polling
// failures so a broken connection fails loudly instead of spinning or
// hiding the error.
const operationPollMaxTransientErrors = 5

// OperationFailedError reports a terminal non-success watch outcome. The
// terminal journal is rendered before this error is returned so the
// command exits nonzero without hiding the terminal detail.
type OperationFailedError struct {
	App     string
	Op      string
	Outcome string
}

func (e *OperationFailedError) Error() string {
	return fmt.Sprintf("operation %s for app %s did not succeed (outcome %s)", e.Op, e.App, e.Outcome)
}

// operationResumeCommand is the concise command that resumes polling an
// existing operation without reissuing the mutation.
func operationResumeCommand(app, key string) string {
	cmd := fmt.Sprintf("gordon apps operations watch %s --key %s", app, key)
	if remoteFlag != "" {
		cmd += " --remote " + remoteFlag
	}
	return cmd
}

// watchOperation polls the by-key journal until the operation is terminal,
// reports progress while it runs, then renders the terminal journal. In
// JSON mode stdout carries exactly one final document and progress goes to
// errOut. A terminal non-success outcome renders the journal and returns
// OperationFailedError.
func watchOperation(ctx context.Context, plane ControlPlane, app, key string, initial *dto.AppDeployResponse, out, errOut io.Writer, jsonOut bool) error {
	if app == "" || key == "" {
		return fmt.Errorf("APP and --key are required")
	}
	progressOut := out
	if jsonOut {
		progressOut = errOut
	}
	op, err := awaitOperation(ctx, plane, app, key, initial, progressOut)
	if err != nil {
		return err
	}
	if jsonOut {
		if err := writeJSON(out, op); err != nil {
			return err
		}
	} else if err := renderAppDeployResponse(out, op); err != nil {
		return err
	}
	if op.Outcome == domain.AppOutcomeSuccess {
		return nil
	}
	return &OperationFailedError{App: app, Op: op.Op, Outcome: op.Outcome}
}

// awaitOperation polls OperationByKey until the journal is terminal. The
// optional initial response (the 202 mutation body) is reported before the
// first poll so its state is not lost. Progress is written only when the
// operation/step fingerprint changes.
func awaitOperation(ctx context.Context, plane ControlPlane, app, key string, initial *dto.AppDeployResponse, progressOut io.Writer) (*dto.AppDeployResponse, error) {
	lastProgress := ""
	transient := 0
	for {
		op := initial
		initial = nil
		if op == nil {
			got, err := plane.OperationByKey(ctx, app, key)
			if err != nil {
				if perr := handlePollError(ctx, progressOut, app, key, err, &transient); perr != nil {
					return nil, perr
				}
				continue
			}
			transient = 0
			op = got
		}
		if op.Status != dto.AppStatusRunning {
			return op, nil
		}
		if sig := operationProgressSignature(op); sig != lastProgress {
			lastProgress = sig
			if err := renderOperationProgress(progressOut, op); err != nil {
				return nil, err
			}
		}
		if err := waitOperationPoll(ctx); err != nil {
			return nil, stopOperationWatch(progressOut, app, key, err)
		}
	}
}

// handlePollError classifies one by-key lookup error. It returns nil when
// the caller should poll again and a terminal error otherwise: 404 is
// terminal, cancellation stops local polling, and any other failure is
// retried up to operationPollMaxTransientErrors consecutive times.
func handlePollError(ctx context.Context, progressOut io.Writer, app, key string, err error, transient *int) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stopOperationWatch(progressOut, app, key, ctxErr)
	}
	if isRemoteNotFoundError(err) {
		return fmt.Errorf("operation %s for app %s is no longer recorded; nothing to resume: %w", key, app, err)
	}
	*transient++
	if *transient > operationPollMaxTransientErrors {
		return fmt.Errorf(
			"gave up watching operation %s for app %s after %d consecutive polling errors: %w; resume with %s",
			key, app, *transient, err, operationResumeCommand(app, key))
	}
	if werr := cliWriteLine(progressOut, cliRenderWarning(fmt.Sprintf(
		"operation watch: transient polling error (%d/%d): %v", *transient, operationPollMaxTransientErrors, err))); werr != nil {
		return werr
	}
	if werr := waitOperationPoll(ctx); werr != nil {
		return stopOperationWatch(progressOut, app, key, werr)
	}
	return nil
}

// waitOperationPoll sleeps for one polling interval, aborting early when
// the command context is cancelled.
func waitOperationPoll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(operationPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// stopOperationWatch reports an interrupted watch without implying any
// server-side cancellation, then returns the interruption as an error. The
// resume command is printed so the user can continue watching later.
func stopOperationWatch(progressOut io.Writer, app, key string, cause error) error {
	if err := cliWriteLine(progressOut, cliRenderWarning("polling stopped; the operation is not cancelled and keeps running on the daemon")); err != nil {
		return err
	}
	if err := cliWriteLine(progressOut, cliRenderMeta("resume:", operationResumeCommand(app, key))); err != nil {
		return err
	}
	return fmt.Errorf("operation watch interrupted: %w", cause)
}

// operationProgressSignature fingerprints the visible operation/step state
// so unchanged polls are not reprinted.
func operationProgressSignature(op *dto.AppDeployResponse) string {
	var b strings.Builder
	b.WriteString(op.Status)
	b.WriteByte(0)
	b.WriteString(op.Outcome)
	for _, step := range op.Steps {
		b.WriteByte(0)
		b.WriteString(step.ID)
		b.WriteByte('=')
		b.WriteString(step.State)
		b.WriteByte(':')
		b.WriteString(step.Detail)
		b.WriteByte(':')
		b.WriteString(step.Error)
	}
	return b.String()
}

// renderOperationProgress writes one concise progress line for the current
// operation/step state.
func renderOperationProgress(out io.Writer, op *dto.AppDeployResponse) error {
	detail := op.Status
	if step, ok := activeOperationStep(op.Steps); ok {
		detail = fmt.Sprintf("%s %s: %s", op.Status, step.ID, step.State)
		if step.Error != "" {
			detail += ": " + sanitizeTerminalText(step.Error)
		} else if step.Detail != "" {
			detail += ": " + sanitizeTerminalText(step.Detail)
		}
	}
	return cliWriteLine(out, cliRenderMeta("op "+op.Op+":", detail))
}

// activeOperationStep returns the step still to run (pending) when one
// exists, otherwise the last journaled step.
func activeOperationStep(steps []dto.AppStepDTO) (dto.AppStepDTO, bool) {
	if len(steps) == 0 {
		return dto.AppStepDTO{}, false
	}
	for _, step := range steps {
		switch step.State {
		case domain.AppStepSucceeded, domain.AppStepFailed, domain.AppStepNotRun:
			continue
		default:
			return step, true
		}
	}
	return steps[len(steps)-1], true
}
