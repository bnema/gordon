package cli

import (
	"fmt"
	"io"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
)

var cliWriteLine = func(w io.Writer, msg string) error {
	_, err := fmt.Fprintln(w, msg)
	return err
}

var cliWritef = func(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func cliRenderTitle(msg string) string {
	return styles.Theme.Title.Render(msg)
}

func cliRenderMuted(msg string) string {
	return styles.Theme.Muted.Render(msg)
}

func cliRenderEmptyState(msg string) string {
	return cliRenderMuted(msg)
}

func cliRenderListItem(msg string) string {
	return styles.RenderListItem(msg, false)
}

func cliRenderMeta(label, value string) string {
	return styles.Theme.Bold.Render(label) + " " + styles.Theme.Muted.Render(value)
}

func cliRenderSuccess(msg string) string {
	return styles.RenderSuccess(msg)
}

func cliRenderWarning(msg string) string {
	return styles.RenderWarning(msg)
}

func cliRenderInfo(msg string) string {
	return styles.RenderInfo(msg)
}
