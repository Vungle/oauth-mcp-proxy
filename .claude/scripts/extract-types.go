// extract-types.go — Produces a type index of all exported symbols per package.
// Run from repo root: go run .claude/scripts/extract-types.go [./...]
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

type FuncInfo struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

type FieldInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Tag  string `json:"tag,omitempty"`
}

type MethodInfo struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Receiver  string `json:"receiver"`
}

type StructInfo struct {
	Name    string      `json:"name"`
	File    string      `json:"file"`
	Line    int         `json:"line"`
	Fields  []FieldInfo `json:"fields"`
	Methods []MethodInfo `json:"methods"`
}

type InterfaceInfo struct {
	Name    string     `json:"name"`
	File    string     `json:"file"`
	Line    int        `json:"line"`
	Methods []FuncInfo `json:"methods"`
}

type ConstInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

type PackageIndex struct {
	Path       string          `json:"path"`
	RelDir     string          `json:"rel_dir"`
	Structs    []StructInfo    `json:"structs,omitempty"`
	Interfaces []InterfaceInfo `json:"interfaces,omitempty"`
	Functions  []FuncInfo      `json:"functions,omitempty"`
	Constants  []ConstInfo     `json:"constants,omitempty"`
}

type Output struct {
	Generated    string         `json:"generated"`
	Module       string         `json:"module"`
	TotalTypes   int            `json:"total_types"`
	TotalFuncs   int            `json:"total_functions"`
	Packages     []PackageIndex `json:"packages"`
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
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedFiles,
		Dir:   repoRoot,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading packages: %v\n", err)
		os.Exit(1)
	}

	modulePath := inferModulePath(pkgs)
	var result []PackageIndex
	totalTypes := 0
	totalFuncs := 0

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		// Skip test packages
		if strings.HasSuffix(pkg.PkgPath, "_test") {
			continue
		}

		idx := PackageIndex{
			Path:   pkg.PkgPath,
			RelDir: relPath(repoRoot, pkgDir(pkg)),
		}

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}

			pos := pkg.Fset.Position(obj.Pos())
			relFile := relPath(repoRoot, pos.Filename)

			switch o := obj.(type) {
			case *types.TypeName:
				named, ok := o.Type().(*types.Named)
				if !ok {
					continue
				}
				totalTypes++

				switch u := named.Underlying().(type) {
				case *types.Struct:
					si := StructInfo{
						Name: name,
						File: relFile,
						Line: pos.Line,
					}
					for i := 0; i < u.NumFields(); i++ {
						f := u.Field(i)
						if f.Exported() {
							si.Fields = append(si.Fields, FieldInfo{
								Name: f.Name(),
								Type: types.TypeString(f.Type(), qualifier(modulePath)),
								Tag:  u.Tag(i),
							})
						}
					}
					// Methods on the named type
					for i := 0; i < named.NumMethods(); i++ {
						m := named.Method(i)
						if m.Exported() {
							sig := m.Type().(*types.Signature)
							recv := "T"
							if _, ok := sig.Recv().Type().(*types.Pointer); ok {
								recv = "*T"
							}
							si.Methods = append(si.Methods, MethodInfo{
								Name:      m.Name(),
								Signature: types.TypeString(m.Type(), qualifier(modulePath)),
								Receiver:  recv,
							})
						}
					}
					idx.Structs = append(idx.Structs, si)

				case *types.Interface:
					ii := InterfaceInfo{
						Name: name,
						File: relFile,
						Line: pos.Line,
					}
					for i := 0; i < u.NumMethods(); i++ {
						m := u.Method(i)
						ii.Methods = append(ii.Methods, FuncInfo{
							Name:      m.Name(),
							Signature: types.TypeString(m.Type(), qualifier(modulePath)),
						})
					}
					idx.Interfaces = append(idx.Interfaces, ii)
				}

			case *types.Func:
				totalFuncs++
				idx.Functions = append(idx.Functions, FuncInfo{
					Name:      name,
					Signature: types.TypeString(o.Type(), qualifier(modulePath)),
					File:      relFile,
					Line:      pos.Line,
				})

			case *types.Const:
				ci := ConstInfo{
					Name: name,
					Type: types.TypeString(o.Type(), qualifier(modulePath)),
				}
				if o.Val() != nil {
					ci.Value = o.Val().ExactString()
				}
				idx.Constants = append(idx.Constants, ci)
			}
		}

		// Only include packages that have exported symbols
		if len(idx.Structs)+len(idx.Interfaces)+len(idx.Functions)+len(idx.Constants) > 0 {
			result = append(result, idx)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	out := Output{
		Generated:  time.Now().UTC().Format(time.RFC3339),
		Module:     modulePath,
		TotalTypes: totalTypes,
		TotalFuncs: totalFuncs,
		Packages:   result,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// qualifier returns a types.Qualifier that shortens the module prefix for readability
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
	// For libraries without internal/pkg/cmd, use the shortest package path
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
	if len(pkg.CompiledGoFiles) > 0 {
		return filepath.Dir(pkg.CompiledGoFiles[0])
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
