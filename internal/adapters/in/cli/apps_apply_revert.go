package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// Set-image revert handling for `apps apply` and `apps diff --file`.
//
// A set-image (CI/OIDC) deploy changes a service image in server desired
// state only. Applying a manifest that still pins the former image would
// restore it, so the daemon refuses such an apply with a 409 unless the
// caller allows it. The CLI explains the change, asks on a terminal, and
// prints the manifest lines that keep the server image.

// readerIsTerminal reports whether r is an interactive terminal.
func readerIsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// resolveSetImageRevert handles an apply refused because it would revert
// set-image changes. Any other error is returned unchanged. With --json the
// conflict is written as JSON and never prompted. On an interactive terminal
// a confirmed revert is retried once against the checked desired revision.
func resolveSetImageRevert(ctx context.Context, plane ControlPlane, req dto.AppApplyRequest, applyErr error, in io.Reader, out io.Writer, opts appsApplyOptions) (*dto.AppApplyResponse, error) {
	var revert *remote.SetImageRevertError
	if !errors.As(applyErr, &revert) {
		return nil, applyErr
	}
	conflict := revert.Conflict
	if opts.JSON {
		if err := writeJSON(out, conflict); err != nil {
			return nil, err
		}
		return nil, setImageRevertRefusal(applyErr)
	}
	if err := renderSetImageRevertWarning(out, conflict.SetImageReverts); err != nil {
		return nil, err
	}
	if !opts.Interactive {
		if err := renderSetImageKeepLines(out, opts.File, conflict.SetImageReverts); err != nil {
			return nil, err
		}
		return nil, setImageRevertRefusal(applyErr)
	}
	confirmed, err := promptYesNo(in, out, "Continue? [y/N] ")
	if err != nil {
		return nil, err
	}
	if !confirmed {
		if err := renderSetImageKeepLines(out, opts.File, conflict.SetImageReverts); err != nil {
			return nil, err
		}
		return nil, errors.New("apply cancelled: the set-image changes were kept; update the manifest with the lines above")
	}
	req.AllowSetImageRevert = true
	req.ExpectedRevision = conflict.DesiredRevision
	return plane.ApplyApp(ctx, req)
}

// setImageRevertRefusal is the non-interactive refusal: it names --yes as the
// way to confirm without a prompt.
func setImageRevertRefusal(cause error) error {
	return fmt.Errorf("apply refused: it would revert set-image changes; update the manifest or rerun with --yes to restore the file images: %w", cause)
}

// promptYesNo asks one question and reads one answer line. Only y/yes
// confirms; an empty answer or end of input means no.
func promptYesNo(in io.Reader, out io.Writer, question string) (bool, error) {
	if err := cliWritef(out, "%s", question); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("failed to read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// renderSetImageRevertWarning explains each set-image change the manifest
// would revert: the server value, its origin, and the file value.
func renderSetImageRevertWarning(out io.Writer, reverts []dto.AppSetImageRevertDTO) error {
	if len(reverts) == 0 {
		return nil
	}
	lines := []string{cliRenderWarning(fmt.Sprintf("Warning: the manifest would revert %d set-image change(s):", len(reverts)))}
	for _, r := range reverts {
		lines = append(lines,
			cliRenderMeta("  service:", r.Service),
			cliRenderMeta("    server:", r.ServerImage+"  ("+setImageOrigin(r)+")"),
			cliRenderMeta("    file:", r.FileImage),
		)
	}
	lines = append(lines, cliRenderMuted("  Applying restores the file value and replaces the running image."))
	for _, line := range lines {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	return nil
}

// setImageOrigin describes where a server image came from.
func setImageOrigin(r dto.AppSetImageRevertDTO) string {
	origin := "set-image, " + r.Revision
	if r.Principal != "" {
		origin += " by " + r.Principal
	}
	if !r.ChangedAt.IsZero() {
		origin += " at " + r.ChangedAt.UTC().Format(time.RFC3339)
	}
	return origin
}

// renderSetImageKeepLines prints the manifest lines that keep the server
// images, in the manifest's [services.<name>] table shape.
func renderSetImageKeepLines(out io.Writer, file string, reverts []dto.AppSetImageRevertDTO) error {
	if len(reverts) == 0 {
		return nil
	}
	target := "your manifest"
	if file != "" {
		target = file
	}
	if err := cliWriteLine(out, cliRenderInfo("To keep the server images, set these lines in "+target+":")); err != nil {
		return err
	}
	for _, r := range reverts {
		if err := cliWritef(out, "\n[services.%s]\nimage = %q\n", r.Service, r.ServerImage); err != nil {
			return err
		}
	}
	return cliWriteLine(out, "")
}

// renderAppApplyPreview renders a dry-run result: the diff with each path
// marked by where it changed, then the set-image warning and the lines that
// keep the server images.
func renderAppApplyPreview(out io.Writer, resp *dto.AppApplyResponse, file string) error {
	if resp.Noop {
		return cliWriteLine(out, cliRenderMuted(fmt.Sprintf("No changes for %s%s", resp.App, appRevisionSuffix(resp.ResultingRevision))))
	}
	if err := cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Validated (dry-run) %s%s", resp.App, appRevisionSuffix(resp.ResultingRevision)))); err != nil {
		return err
	}
	if err := renderMarkedDiffSection(out, resp.Diff, resp.SetImageReverts); err != nil {
		return err
	}
	if err := renderSetImageRevertWarning(out, resp.SetImageReverts); err != nil {
		return err
	}
	return renderSetImageKeepLines(out, file, resp.SetImageReverts)
}

// renderMarkedDiffSection renders the diff paths like renderAppDiffSection and
// marks each one: a set-image revert changed on the server, any other path
// changed in the file.
func renderMarkedDiffSection(out io.Writer, diff dto.AppDiffSection, reverts []dto.AppSetImageRevertDTO) error {
	serverPaths := make(map[string]string, len(reverts))
	for _, r := range reverts {
		serverPaths["service/"+r.Service+"/image"] = r.Revision
	}
	return walkDiffSection(diff, func(title, path string) error {
		mark := "(changed in your file)"
		if rev, ok := serverPaths[path]; ok {
			mark = "(changed on the server (set-image, " + rev + "))"
		}
		return cliWriteLine(out, cliRenderMeta(title, path+"  "+mark))
	})
}

// renderAppliedReverts notes the set-image changes a confirmed apply replaced.
func renderAppliedReverts(out io.Writer, reverts []dto.AppSetImageRevertDTO) error {
	for _, r := range reverts {
		line := fmt.Sprintf("%s %s -> %s (was set-image, %s)", r.Service, r.ServerImage, r.FileImage, r.Revision)
		if err := cliWriteLine(out, cliRenderMeta("restored:", line)); err != nil {
			return err
		}
	}
	return nil
}
