// internal-deps.go — Builds the internal package dependency DAG.
// Run from repo root: go run .claude/scripts/internal-deps.go [./...]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

type PackageNode struct {
	Path       string   `json:"path"`
	RelDir     string   `json:"rel_dir"`
	Imports    []string `json:"imports"`
	ImportedBy []string `json:"imported_by"`
	FileCount  int      `json:"file_count"`
}

type Output struct {
	Generated    string                 `json:"generated"`
	Module       string                 `json:"module"`
	TotalPkgs    int                    `json:"total_packages"`
	Packages     map[string]PackageNode `json:"packages"`
	ExternalDeps []string               `json:"external_deps"`
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
		Mode:  packages.NeedName | packages.NeedImports | packages.NeedFiles,
		Dir:   repoRoot,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading packages: %v\n", err)
		os.Exit(1)
	}

	modulePath := inferModulePath(pkgs)
	nodes := make(map[string]PackageNode)
	externalSet := make(map[string]struct{})

	// First pass: collect all internal packages and their imports
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.PkgPath, "_test") {
			continue
		}
		if !strings.HasPrefix(pkg.PkgPath, modulePath+"/") && pkg.PkgPath != modulePath {
			continue
		}

		relPkg := pkg.PkgPath
		if pkg.PkgPath != modulePath {
			relPkg = strings.TrimPrefix(pkg.PkgPath, modulePath+"/")
		} else {
			relPkg = "."
		}

		var internalImports []string
		for imp := range pkg.Imports {
			if strings.HasPrefix(imp, modulePath+"/") {
				internalImports = append(internalImports, strings.TrimPrefix(imp, modulePath+"/"))
			} else if imp == modulePath {
				internalImports = append(internalImports, ".")
			} else if !isStdLib(imp) {
				externalSet[imp] = struct{}{}
			}
		}
		sort.Strings(internalImports)

		pDir := ""
		if len(pkg.GoFiles) > 0 {
			pDir = relPath(repoRoot, filepath.Dir(pkg.GoFiles[0]))
		}

		nodes[relPkg] = PackageNode{
			Path:      relPkg,
			RelDir:    pDir,
			Imports:   internalImports,
			FileCount: len(pkg.GoFiles),
		}
	}

	// Second pass: compute imported_by (reverse edges)
	for pkgName, node := range nodes {
		for _, imp := range node.Imports {
			if target, ok := nodes[imp]; ok {
				target.ImportedBy = append(target.ImportedBy, pkgName)
				nodes[imp] = target
			}
		}
	}

	// Sort imported_by
	for k, node := range nodes {
		sort.Strings(node.ImportedBy)
		nodes[k] = node
	}

	// External deps sorted
	var externalDeps []string
	for dep := range externalSet {
		externalDeps = append(externalDeps, dep)
	}
	sort.Strings(externalDeps)

	out := Output{
		Generated:    time.Now().UTC().Format(time.RFC3339),
		Module:       modulePath,
		TotalPkgs:    len(nodes),
		Packages:     nodes,
		ExternalDeps: externalDeps,
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

// isStdLib is a rough heuristic: std lib packages have no dots in the first path element.
func isStdLib(path string) bool {
	first := path
	if i := strings.IndexByte(path, '/'); i >= 0 {
		first = path[:i]
	}
	return !strings.Contains(first, ".")
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
