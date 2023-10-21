package cli

import (
	"log"

	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/bnema/gordon/internal/cli/mvu"
	tea "github.com/charmbracelet/bubbletea"
)



func InitCli(a *app.App, m *mvu.Model)) {
	p := tea.NewProgram(Init, mvu.Init, mvu.Update, mvu.View)
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}
