// extract-concurrency.go — Scans for concurrency patterns: goroutine spawns,
// channels, mutexes, context propagation, and sync primitives.
// Run from repo root: go run .claude/scripts/extract-concurrency.go [./...]
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

type GoroutineSpawn struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Expr     string `json:"expr"`
}

type ChannelUsage struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Kind    string `json:"kind"` // "make", "send", "receive", "declaration"
}

type MutexUsage struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Name   string `json:"name"`
	Type   string `json:"type"` // "Mutex", "RWMutex", "Once", "WaitGroup"
	Action string `json:"action"` // "declaration", "Lock", "Unlock", "RLock", "RUnlock", "Do", "Add", "Wait", "Done"
}

type ContextUsage struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Pattern  string `json:"pattern"` // "WithCancel", "WithTimeout", "WithDeadline", "WithValue", "Background", "TODO", "param"
}

type PackageSummary struct {
	Package    string `json:"package"`
	Goroutines int    `json:"goroutines"`
	Channels   int    `json:"channels"`
	Mutexes    int    `json:"mutexes"`
	Contexts   int    `json:"contexts"`
}

type Output struct {
	Generated       string           `json:"generated"`
	Module          string           `json:"module"`
	Summary         Summary          `json:"summary"`
	Goroutines      []GoroutineSpawn `json:"goroutines"`
	Channels        []ChannelUsage   `json:"channels"`
	Mutexes         []MutexUsage     `json:"mutexes"`
	Contexts        []ContextUsage   `json:"contexts"`
	PackageSummary  []PackageSummary `json:"package_summary"`
}

type Summary struct {
	TotalGoroutines int `json:"total_goroutines"`
	TotalChannels   int `json:"total_channels"`
	TotalMutexes    int `json:"total_mutexes"`
	TotalContexts   int `json:"total_contexts"`
	TotalPackages   int `json:"packages_with_concurrency"`
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
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   repoRoot,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading packages: %v\n", err)
		os.Exit(1)
	}

	modulePath := inferModulePath(pkgs)

	var goroutines []GoroutineSpawn
	var channels []ChannelUsage
	var mutexes []MutexUsage
	var contexts []ContextUsage
	pkgCounts := make(map[string]*PackageSummary)

	for _, pkg := range pkgs {
		if !strings.HasPrefix(pkg.PkgPath, modulePath) {
			continue
		}
		if strings.HasSuffix(pkg.PkgPath, "_test") {
			continue
		}

		shortPkg := strings.TrimPrefix(pkg.PkgPath, modulePath+"/")
		if pkg.PkgPath == modulePath {
			shortPkg = "."
		}
		summary := &PackageSummary{Package: shortPkg}

		for _, file := range pkg.Syntax {
			filename := pkg.Fset.Position(file.Pos()).Filename
			relFile := relPath(repoRoot, filename)

			// Skip test files
			if strings.HasSuffix(relFile, "_test.go") {
				continue
			}

			enclosingFunc := ""

			ast.Inspect(file, func(n ast.Node) bool {
				if n == nil {
					return false
				}

				// Track enclosing function
				switch fn := n.(type) {
				case *ast.FuncDecl:
					enclosingFunc = fn.Name.Name
					if fn.Recv != nil && len(fn.Recv.List) > 0 {
						enclosingFunc = exprString(fn.Recv.List[0].Type) + "." + fn.Name.Name
					}
				}

				pos := pkg.Fset.Position(n.Pos())

				switch node := n.(type) {
				// Goroutine spawns: go expr
				case *ast.GoStmt:
					goroutines = append(goroutines, GoroutineSpawn{
						File:     relFile,
						Line:     pos.Line,
						Function: enclosingFunc,
						Expr:     truncate(exprString(node.Call.Fun), 80),
					})
					summary.Goroutines++

				// Channel makes: make(chan T)
				case *ast.CallExpr:
					if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "make" {
						if len(node.Args) > 0 {
							if ch, ok := node.Args[0].(*ast.ChanType); ok {
								channels = append(channels, ChannelUsage{
									File: relFile,
									Line: pos.Line,
									Type: exprString(ch.Value),
									Kind: "make",
								})
								summary.Channels++
							}
						}
					}

					// Context creation patterns
					if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
						selName := sel.Sel.Name
						switch selName {
						case "WithCancel", "WithTimeout", "WithDeadline", "WithValue", "Background", "TODO":
							if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "context" {
								contexts = append(contexts, ContextUsage{
									File:     relFile,
									Line:     pos.Line,
									Function: enclosingFunc,
									Pattern:  selName,
								})
								summary.Contexts++
							}
						}

						// Mutex operations
						switch selName {
						case "Lock", "Unlock", "RLock", "RUnlock":
							mutexes = append(mutexes, MutexUsage{
								File:   relFile,
								Line:   pos.Line,
								Name:   exprString(sel.X),
								Type:   inferMutexType(selName),
								Action: selName,
							})
							summary.Mutexes++
						case "Do":
							// sync.Once.Do
							mutexes = append(mutexes, MutexUsage{
								File:   relFile,
								Line:   pos.Line,
								Name:   exprString(sel.X),
								Type:   "Once",
								Action: "Do",
							})
							summary.Mutexes++
						case "Add", "Done", "Wait":
							nameStr := exprString(sel.X)
							if strings.Contains(strings.ToLower(nameStr), "wg") ||
								strings.Contains(strings.ToLower(nameStr), "wait") ||
								strings.Contains(strings.ToLower(nameStr), "group") {
								mutexes = append(mutexes, MutexUsage{
									File:   relFile,
									Line:   pos.Line,
									Name:   nameStr,
									Type:   "WaitGroup",
									Action: selName,
								})
								summary.Mutexes++
							}
						}

					}

				// Channel send
				case *ast.SendStmt:
					channels = append(channels, ChannelUsage{
						File: relFile,
						Line: pos.Line,
						Name: exprString(node.Chan),
						Kind: "send",
					})
					summary.Channels++

				// Channel receive: <-ch
				case *ast.UnaryExpr:
					if node.Op == token.ARROW {
						channels = append(channels, ChannelUsage{
							File: relFile,
							Line: pos.Line,
							Name: exprString(node.X),
							Kind: "receive",
						})
						summary.Channels++
					}

				// Field declarations with sync types
				case *ast.Field:
					typeStr := exprString(node.Type)
					if strings.Contains(typeStr, "sync.Mutex") ||
						strings.Contains(typeStr, "sync.RWMutex") ||
						strings.Contains(typeStr, "sync.Once") ||
						strings.Contains(typeStr, "sync.WaitGroup") {
						name := ""
						if len(node.Names) > 0 {
							name = node.Names[0].Name
						}
						mutexes = append(mutexes, MutexUsage{
							File:   relFile,
							Line:   pos.Line,
							Name:   name,
							Type:   strings.TrimPrefix(typeStr, "sync."),
							Action: "declaration",
						})
						summary.Mutexes++
					}

					// Channel field declarations
					if _, ok := node.Type.(*ast.ChanType); ok {
						name := ""
						if len(node.Names) > 0 {
							name = node.Names[0].Name
						}
						channels = append(channels, ChannelUsage{
							File: relFile,
							Line: pos.Line,
							Name: name,
							Kind: "declaration",
						})
						summary.Channels++
					}
				}

				return true
			})
		}

		if summary.Goroutines+summary.Channels+summary.Mutexes+summary.Contexts > 0 {
			pkgCounts[shortPkg] = summary
		}
	}

	// Build package summaries sorted by total concurrency usage
	var pkgSummaries []PackageSummary
	for _, s := range pkgCounts {
		pkgSummaries = append(pkgSummaries, *s)
	}
	sort.Slice(pkgSummaries, func(i, j int) bool {
		ti := pkgSummaries[i].Goroutines + pkgSummaries[i].Channels + pkgSummaries[i].Mutexes
		tj := pkgSummaries[j].Goroutines + pkgSummaries[j].Channels + pkgSummaries[j].Mutexes
		return ti > tj
	})

	out := Output{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Module:    modulePath,
		Summary: Summary{
			TotalGoroutines: len(goroutines),
			TotalChannels:   len(channels),
			TotalMutexes:    len(mutexes),
			TotalContexts:   len(contexts),
			TotalPackages:   len(pkgSummaries),
		},
		Goroutines:     goroutines,
		Channels:       channels,
		Mutexes:        mutexes,
		Contexts:       contexts,
		PackageSummary: pkgSummaries,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

func exprString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.IndexExpr:
		return exprString(e.X) + "[" + exprString(e.Index) + "]"
	case *ast.FuncLit:
		return "func literal"
	case *ast.CallExpr:
		return exprString(e.Fun) + "(...)"
	case *ast.ParenExpr:
		return "(" + exprString(e.X) + ")"
	case *ast.UnaryExpr:
		return e.Op.String() + exprString(e.X)
	default:
		return fmt.Sprintf("<%T>", expr)
	}
}

func inferMutexType(action string) string {
	switch action {
	case "RLock", "RUnlock":
		return "RWMutex"
	default:
		return "Mutex"
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
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
