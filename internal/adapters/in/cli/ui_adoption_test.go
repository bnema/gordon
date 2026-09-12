package cli

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
)

// uiAdoptionSeamMu guards mutations of the package-level cliWriteLine and
// cliWritef variables. Any test that overrides these seams must hold this
// mutex for the duration of the override and restore the originals on cleanup.
var uiAdoptionSeamMu sync.Mutex

func TestPresentationHelpers(t *testing.T) {
	if got := cliRenderTitle("Title"); got == "" {
		t.Fatal("cliRenderTitle returned empty output")
	}
	if got := cliRenderEmptyState("none"); got == "" {
		t.Fatal("cliRenderEmptyState returned empty output")
	}
	if got := cliRenderSuccess("ok"); got == "" {
		t.Fatal("cliRenderSuccess returned empty output")
	}
	if got := cliRenderWarning("warn"); got == "" {
		t.Fatal("cliRenderWarning returned empty output")
	}
	if got := cliRenderInfo("info"); got == "" {
		t.Fatal("cliRenderInfo returned empty output")
	}
	if got := cliRenderListItem("item"); got == "" {
		t.Fatal("cliRenderListItem returned empty output")
	}
}

func TestUIAdoptionRuntimeSeams(t *testing.T) {
	uiAdoptionSeamMu.Lock()
	defer uiAdoptionSeamMu.Unlock()

	origWriteLine := cliWriteLine
	origWritef := cliWritef
	defer func() {
		cliWriteLine = origWriteLine
		cliWritef = origWritef
	}()

	lineCalls := 0
	writefCalls := 0
	cliWriteLine = func(w io.Writer, msg string) error {
		lineCalls++
		_, err := io.WriteString(w, msg+"\n")
		return err
	}
	cliWritef = func(w io.Writer, format string, args ...any) error {
		writefCalls++
		_, err := io.WriteString(w, "formatted\n")
		return err
	}

	versionCmd := newVersionCmd()
	versionCmd.SetArgs(nil)
	versionCmd.SetOut(new(bytes.Buffer))
	if err := versionCmd.Execute(); err != nil {
		t.Fatalf("version command execution failed: %v", err)
	}

	imagesMock := climocks.NewMockimagesClient(t)
	imagesMock.EXPECT().ListImages(context.Background()).Return([]dto.Image{{Repository: "repo/app", Tag: "latest", ID: "sha256:abc", Size: 1}}, nil).Once()

	imgBuf := new(bytes.Buffer)
	if err := runImagesList(context.Background(), imagesMock, imgBuf, false); err != nil {
		t.Fatalf("runImagesList failed: %v", err)
	}

	pruneMock := climocks.NewMockimagesClient(t)
	pruneMock.EXPECT().PruneImages(context.Background(), mock.Anything).Return(&dto.ImagePruneResponse{Plan: dto.PruneSummary{}}, nil).Once()

	pruneBuf := new(bytes.Buffer)
	if err := runImagesPrune(context.Background(), pruneMock, imagesPruneOptions{DryRun: true}, pruneBuf); err != nil {
		t.Fatalf("runImagesPrune failed: %v", err)
	}

	if lineCalls == 0 {
		t.Fatal("expected cliWriteLine to be called")
	}
	if writefCalls == 0 {
		t.Fatal("expected cliWritef to be called")
	}
}
