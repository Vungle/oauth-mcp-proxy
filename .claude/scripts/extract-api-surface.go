// extract-api-surface.go — Extracts the exported public API per package.
// Focused on what consumers (other repos/packages) can use.
// Run from repo root: go run .claude/scripts/extract-api-surface.go [./...]
package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

type APIFunc struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

type APIType struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // struct, interface, alias, other
	Methods []APIFunc `json:"methods,omitempty"`
}

type APIConst struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

type APIVar struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type PackageAPI struct {
	Path      string     `json:"path"`
	RelDir    string     `json:"rel_dir"`
	Types     []APIType  `json:"types,omitempty"`
	Functions []APIFunc  `json:"functions,omitempty"`
	Constants []APIConst `json:"constants,omitempty"`
	Variables []APIVar   `json:"variables,omitempty"`
}

type Output struct {
	Generated string       `json:"generated"`
	Module    string       `json:"module"`
	Packages  []PackageAPI `json:"packages"`
}

func main() {
	repoRoot, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error finding repo root: %v\n", err)
		os.Exit(1)
	}

	patterns := []string{"./..."}
	if len(os.Args) > 1 {
		patterns = os.Args[1:]
	}

	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedTypes | packages.NeedFiles,
		Dir:   repoRoot,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading packages: %v\n", err)
		os.Exit(1)
	}

	modulePath := inferModulePath(pkgs)
	var result []PackageAPI

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		if strings.HasSuffix(pkg.PkgPath, "_test") {
			continue
		}

		api := PackageAPI{
			Path:   pkg.PkgPath,
			RelDir: relPath(repoRoot, pkgDir(pkg)),
		}

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}

			switch o := obj.(type) {
			case *types.TypeName:
				named, ok := o.Type().(*types.Named)
				if !ok {
					continue
				}

				at := APIType{Name: name}
				switch named.Underlying().(type) {
				case *types.Struct:
					at.Kind = "struct"
				case *types.Interface:
					at.Kind = "interface"
				default:
					at.Kind = "alias"
				}

				for i := 0; i < named.NumMethods(); i++ {
					m := named.Method(i)
					if m.Exported() {
						at.Methods = append(at.Methods, APIFunc{
							Name:      m.Name(),
							Signature: types.TypeString(m.Type(), qualifier(modulePath)),
						})
					}
				}
				api.Types = append(api.Types, at)

			case *types.Func:
				api.Functions = append(api.Functions, APIFunc{
					Name:      name,
					Signature: types.TypeString(o.Type(), qualifier(modulePath)),
				})

			case *types.Const:
				ci := APIConst{
					Name: name,
					Type: types.TypeString(o.Type(), qualifier(modulePath)),
				}
				if o.Val() != nil {
					ci.Value = o.Val().ExactString()
				}
				api.Constants = append(api.Constants, ci)

			case *types.Var:
				api.Variables = append(api.Variables, APIVar{
					Name: name,
					Type: types.TypeString(o.Type(), qualifier(modulePath)),
				})
			}
		}

		if len(api.Types)+len(api.Functions)+len(api.Constants)+len(api.Variables) > 0 {
			result = append(result, api)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	out := Output{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Module:    modulePath,
		Packages:  result,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

func qualifier(modulePath string) types.Qualifier {
	return func(pkg *types.Package) string {
		p := pkg.Path()
		if strings.HasPrefix(p, modulePath+"/") {
			return strings.TrimPrefix(p, modulePath+"/")
		}
		return p
	}
}

func inferModulePath(pkgs []*packages.Package) string {
	for _, pkg := range pkgs {
		for _, prefix := range []string{"/internal/", "/pkg/", "/cmd/"} {
			parts := strings.SplitN(pkg.PkgPath, prefix, 2)
			if len(parts) == 2 {
				return parts[0]
			}
		}
	}
	if len(pkgs) > 0 {
		shortest := pkgs[0].PkgPath
		for _, pkg := range pkgs[1:] {
			if len(pkg.PkgPath) < len(shortest) {
				shortest = pkg.PkgPath
			}
		}
		return shortest
	}
	return ""
}

func pkgDir(pkg *packages.Package) string {
	if len(pkg.GoFiles) > 0 {
		return filepath.Dir(pkg.GoFiles[0])
	}
	return ""
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if !isInsideDotClaude(dir) {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	wd, _ := os.Getwd()
	return wd, nil
}

func isInsideDotClaude(dir string) bool {
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if filepath.Base(d) == ".claude" {
			return true
		}
	}
	return false
}

func relPath(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
