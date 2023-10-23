package main

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/cli"
)

func main() {
	a := app.NewClientApp()
	cli.Execute(a)
}
