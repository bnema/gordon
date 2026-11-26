package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// finishAppMutation completes an app mutation the daemon accepted. A running
// operation is watched to terminal through the by-key journal. A terminal
// response is rendered, and a non-success outcome returns
// OperationFailedError.
func finishAppMutation(ctx context.Context, plane ControlPlane, app, key string, resp *dto.AppDeployResponse, out, errOut io.Writer, jsonOut bool) error {
	if resp.Status == dto.AppStatusRunning {
		return watchOperation(ctx, plane, app, key, resp, out, errOut, jsonOut)
	}
	if jsonOut {
		if err := writeJSON(out, resp); err != nil {
			return err
		}
	} else if err := renderAppDeployResponse(out, resp); err != nil {
		return err
	}
	if resp.Outcome != domain.AppOutcomeSuccess {
		return &OperationFailedError{App: app, Op: resp.Op, Outcome: resp.Outcome}
	}
	return nil
}

// renderAcceptedOperation reports a running operation the command stops
// following. JSON mode prints the accepted journal, whose op is the request
// key; the human output names the key and the commands that read the
// outcome later.
func renderAcceptedOperation(out io.Writer, verb, app, key string, resp *dto.AppDeployResponse, jsonOut bool) error {
	if jsonOut {
		return writeJSON(out, resp)
	}
	if err := renderAppDeployResponse(out, resp); err != nil {
		return err
	}
	lines := []string{
		cliRenderSuccess(fmt.Sprintf("%s of %s accepted; it keeps running on the daemon", verb, app)),
		cliRenderMeta("key:", key),
		cliRenderMeta("outcome:", operationCommand("show", app, key)),
		cliRenderMeta("follow:", operationCommand("watch", app, key)),
	}
	for _, line := range lines {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	return nil
}
