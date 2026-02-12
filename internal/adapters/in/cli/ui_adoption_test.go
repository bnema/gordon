package cli

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/bnema/gordon/internal/adapters/dto"
)

var uiAdoptionHelperCalls = map[string]struct{}{
	"cliWriteLine":        {},
	"cliWritef":           {},
	"cliRenderTitle":      {},
	"cliRenderMuted":      {},
	"cliRenderEmptyState": {},
	"cliRenderListItem":   {},
	"cliRenderMeta":       {},
	"cliRenderSuccess":    {},
	"cliRenderWarning":    {},
	"cliRenderInfo":       {},
}

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

func TestUIAdoption(t *testing.T) {
	for _, expect := range uiAdoptionExpectations {
		expect := expect
		t.Run(expect.family, func(t *testing.T) {
			fset := token.NewFileSet()
			fileNode, err := parser.ParseFile(fset, expect.file, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", expect.file, err)
			}

			for _, fnName := range expect.functions {
				fnName := fnName
				t.Run(fnName, func(t *testing.T) {
					fn := findFuncDecl(fileNode, fnName)
					if fn == nil {
						t.Fatalf("function %s not found in %s", fnName, expect.file)
					}

					hasHelperCall := false
					hasSharedUIUsage := false
					hasForbiddenRawPrint := false

					ast.Inspect(fn.Body, func(n ast.Node) bool {
						call, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}

						switch fun := call.Fun.(type) {
						case *ast.Ident:
							if _, ok := uiAdoptionHelperCalls[fun.Name]; ok {
								hasHelperCall = true
							}
						case *ast.SelectorExpr:
							if isForbiddenRawPrintCall(call, fun) {
								hasForbiddenRawPrint = true
							}
							pkgIdent, ok := fun.X.(*ast.Ident)
							if !ok {
								return true
							}
							if pkgIdent.Name == "styles" || pkgIdent.Name == "components" {
								hasSharedUIUsage = true
							}
						}

						return true
					})

					if !hasHelperCall && !hasSharedUIUsage {
						t.Fatalf("%s in %s does not use presentation helpers or shared ui styles/components", fnName, expect.file)
					}

					if hasForbiddenRawPrint {
						t.Fatalf("%s in %s still uses forbidden raw print calls", fnName, expect.file)
					}
				})
			}
		})
	}
}

func isForbiddenRawPrintCall(call *ast.CallExpr, sel *ast.SelectorExpr) bool {
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	if pkgIdent.Name == "fmt" {
		switch sel.Sel.Name {
		case "Print", "Printf", "Println", "Fprint", "Fprintf", "Fprintln":
			if sel.Sel.Name == "Print" || sel.Sel.Name == "Printf" || sel.Sel.Name == "Println" {
				return true
			}

			if len(call.Args) == 0 {
				return true
			}

			switch dst := call.Args[0].(type) {
			case *ast.SelectorExpr:
				if dstPkg, ok := dst.X.(*ast.Ident); ok && dstPkg.Name == "os" && (dst.Sel.Name == "Stdout" || dst.Sel.Name == "Stderr") {
					return true
				}
			case *ast.Ident:
				if dst.Name == "out" {
					return true
				}
			}
		}
	}

	if pkgIdent.Name == "cmd" && strings.HasPrefix(sel.Sel.Name, "Print") {
		return true
	}

	return false
}

type imagesClientStub struct {
	listResp  []dto.Image
	pruneResp *dto.ImagePruneResponse
	err       error
}

func (s imagesClientStub) ListImages(context.Context) ([]dto.Image, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.listResp, nil
}

func (s imagesClientStub) PruneImages(context.Context, int) (*dto.ImagePruneResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.pruneResp, nil
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

	imgBuf := new(bytes.Buffer)
	if err := runImagesList(context.Background(), imagesClientStub{listResp: []dto.Image{{Repository: "repo/app", Tag: "latest", ID: "sha256:abc", Size: 1}}}, imgBuf); err != nil {
		t.Fatalf("runImagesList failed: %v", err)
	}

	pruneBuf := new(bytes.Buffer)
	if err := runImagesPrune(context.Background(), imagesClientStub{listResp: []dto.Image{}, pruneResp: &dto.ImagePruneResponse{}}, imagesPruneOptions{DryRun: true}, pruneBuf); err != nil {
		t.Fatalf("runImagesPrune failed: %v", err)
	}

	if lineCalls == 0 {
		t.Fatal("expected cliWriteLine to be called")
	}
	if writefCalls == 0 {
		t.Fatal("expected cliWritef to be called")
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == name {
			return fn
		}
	}
	return nil
}
