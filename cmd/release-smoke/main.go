package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bnema/gordon/internal/testutils/releasesmoke"
)

func main() {
	dist := flag.String("dist", "./dist", "GoReleaser dist directory with artifacts.json")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: release-smoke [-dist dir] image|podman-managed-pass\n")
		os.Exit(2)
	}

	h := releasesmoke.NewHarness(*dist)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch flag.Arg(0) {
	case "image":
		err = h.RunImageSmoke(ctx)
	case "podman-managed-pass":
		err = h.RunPodmanManagedPassSmoke(ctx)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", flag.Arg(0))
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
