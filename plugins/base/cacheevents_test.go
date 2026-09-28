package alborzbase

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

var cacheTypes = map[string]string{"listings": "listingCache", "bodies": "bodyCache"}

// Everything unlisted is denied, so a method added later is guarded from the start.
var cacheDirect = map[string][]string{
	"listingCache": {"lookup", "message", "load", "claim", "release", "keeps", "epoch", "pageSize", "heldSize", "storeAt", "fill", "refresh"},
	"bodyCache":    {"current", "claim", "put"},
}

// Patches assert a claim, so they belong on the connection turn that confirmed it.
var cachePatches = map[string]bool{"messagesFlagged": true, "messageRead": true}

func parsePackage(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return fset, files
}

func receiver(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if name, ok := expr.(*ast.Ident); ok {
		return name.Name
	}
	return ""
}

func TestCacheWritesGoThroughEvents(t *testing.T) {
	fset, files := parsePackage(t)

	methods := map[string]map[string]bool{}
	for _, typ := range cacheTypes {
		methods[typ] = map[string]bool{}
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && methods[receiver(fn)] != nil {
				methods[receiver(fn)][fn.Name.Name] = true
			}
		}
	}
	direct := map[string]map[string]bool{}
	for typ, names := range cacheDirect {
		direct[typ] = map[string]bool{}
		for _, name := range names {
			if !methods[typ][name] {
				t.Errorf("%s.%s is let through but no longer exists", typ, name)
			}
			direct[typ][name] = true
		}
	}

	viaEvents := 0
	for _, file := range files {
		name := filepath.Base(fset.Position(file.Pos()).Filename)
		// The files the caches live in may use their fields; nobody else.
		home := name == "cache.go" || name == "bodies.go"
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || methods[receiver(fn)] != nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				cache, ok := sel.X.(*ast.SelectorExpr)
				typ := ""
				if ok {
					typ = cacheTypes[cache.Sel.Name]
				}
				if typ == "" {
					return true
				}
				member := sel.Sel.Name
				switch {
				case name == "cacheevents.go":
					viaEvents++
				case direct[typ][member]:
				case home && !methods[typ][member]:
				default:
					t.Errorf("%s:%d: %s.%s in %s bypasses cacheevents.go, so the other cache is left to the caller",
						name, fset.Position(sel.Pos()).Line, cache.Sel.Name, member, fn.Name.Name)
				}
				return true
			})
		}
	}
	if viaEvents == 0 {
		t.Fatal("cacheevents.go touches no cache: a rename left this check watching nothing")
	}
}

func takesConnection(fn *ast.FuncType) bool {
	for _, param := range fn.Params.List {
		star, ok := param.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Client" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "imapclient" {
				return true
			}
		}
	}
	return false
}

func TestCachePatchesStayOnTheConnectionTurn(t *testing.T) {
	fset, files := parsePackage(t)
	found := 0
	for _, file := range files {
		type span struct{ from, to token.Pos }
		var turns []span
		ast.Inspect(file, func(n ast.Node) bool {
			// A function handed the connection runs on the turn that holds it.
			if fn, ok := n.(*ast.FuncDecl); ok && takesConnection(fn.Type) {
				turns = append(turns, span{fn.Pos(), fn.End()})
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "DoIMAP") {
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						turns = append(turns, span{lit.Pos(), lit.End()})
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			patch, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !cachePatches[patch.Sel.Name] {
				return true
			}
			found++
			for _, turn := range turns {
				if turn.from <= call.Pos() && call.End() <= turn.to {
					return true
				}
			}
			at := fset.Position(call.Pos())
			t.Errorf("%s:%d: %s outside the DoIMAP callback that confirmed it: a write landing in between is patched over",
				filepath.Base(at.Filename), at.Line, patch.Sel.Name)
			return true
		})
	}
	if found == 0 {
		t.Fatal("no patch is called anywhere: a rename left this check watching nothing")
	}
}
