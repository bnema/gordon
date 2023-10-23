package main

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/cli"
)

func main() {
	a, err := app.NewClientApp()
	if err != nil {
		fmt.Println("Error initializing app:", err)
	}
	cli.Execute(a)
}
