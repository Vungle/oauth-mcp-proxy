// extract-interfaces.go — Extracts all interface definitions and their implementors.
// Run from repo root: go run .claude/scripts/extract-interfaces.go [./...]
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

type Method struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

type InterfaceEntry struct {
	Name        string        `json:"name"`
	Package     string        `json:"package"`
	File        string        `json:"file"`
	Line        int           `json:"line"`
	Methods     []Method      `json:"methods"`
	Implementors []Implementor `json:"implementors"`
}

type Implementor struct {
	Name    string `json:"name"`
	Package string `json:"package"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Pointer bool   `json:"pointer"` // true if *T implements, not T
}

type Output struct {
	Generated  string           `json:"generated"`
	Module     string           `json:"module"`
	Interfaces []InterfaceEntry `json:"interfaces"`
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

	// Collect all interfaces and all named types
	type ifaceInfo struct {
		name    string
		pkg     string
		file    string
		line    int
		iface   *types.Interface
		methods []Method
	}

	type namedInfo struct {
		name string
		pkg  string
		file string
		line int
		typ  *types.Named
	}

	var ifaces []ifaceInfo
	var namedTypes []namedInfo
	modulePath := inferModulePath(pkgs)

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok || !tn.Exported() {
				continue
			}

			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}

			pos := pkg.Fset.Position(obj.Pos())
			relFile := relPath(repoRoot, pos.Filename)

			if iface, ok := named.Underlying().(*types.Interface); ok {
				var methods []Method
				for i := 0; i < iface.NumMethods(); i++ {
					m := iface.Method(i)
					methods = append(methods, Method{
						Name:      m.Name(),
						Signature: types.TypeString(m.Type(), nil),
					})
				}
				ifaces = append(ifaces, ifaceInfo{
					name:    name,
					pkg:     pkg.PkgPath,
					file:    relFile,
					line:    pos.Line,
					iface:   iface,
					methods: methods,
				})
			}

			namedTypes = append(namedTypes, namedInfo{
				name: name,
				pkg:  pkg.PkgPath,
				file: relFile,
				line: pos.Line,
				typ:  named,
			})
		}
	}

	// For each interface, find implementors
	var entries []InterfaceEntry
	for _, ifc := range ifaces {
		if ifc.iface.NumMethods() == 0 {
			continue // skip empty interfaces
		}

		entry := InterfaceEntry{
			Name:    ifc.name,
			Package: ifc.pkg,
			File:    ifc.file,
			Line:    ifc.line,
			Methods: ifc.methods,
		}

		for _, nt := range namedTypes {
			// Skip the interface itself
			if nt.pkg == ifc.pkg && nt.name == ifc.name {
				continue
			}

			// Check if T implements the interface
			if types.Implements(nt.typ, ifc.iface) {
				entry.Implementors = append(entry.Implementors, Implementor{
					Name:    nt.name,
					Package: nt.pkg,
					File:    nt.file,
					Line:    nt.line,
					Pointer: false,
				})
			} else if ptr := types.NewPointer(nt.typ); types.Implements(ptr, ifc.iface) {
				entry.Implementors = append(entry.Implementors, Implementor{
					Name:    nt.name,
					Package: nt.pkg,
					File:    nt.file,
					Line:    nt.line,
					Pointer: true,
				})
			}
		}

		sort.Slice(entry.Implementors, func(i, j int) bool {
			return entry.Implementors[i].Package+"."+entry.Implementors[i].Name <
				entry.Implementors[j].Package+"."+entry.Implementors[j].Name
		})

		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Package+"."+entries[i].Name <
			entries[j].Package+"."+entries[j].Name
	})

	out := Output{
		Generated:  time.Now().UTC().Format(time.RFC3339),
		Module:     modulePath,
		Interfaces: entries,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
		os.Exit(1)
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
