package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/app"
	tea "github.com/charmbracelet/bubbletea"
)

type statusMsg int
type timeout time.Duration

func CheckServer(a *app.App) tea.Msg {
	url := a.Config.Admin.Path
	c := &http.Client{
		Timeout: 10 * time.Second,
	}
	res, err := c.Get(url)
	if err != nil {
		fmt.Println(err)
		return mvu.ErrMsg{Err: err}
	}
	defer res.Body.Close() // nolint:errcheck

	return statusMsg(res.StatusCode)
}
