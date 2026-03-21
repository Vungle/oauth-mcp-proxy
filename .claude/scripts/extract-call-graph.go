// extract-call-graph.go — Builds a function call graph using VTA (Variable Type Analysis).
// Produces caller->callee edges for all packages in the module.
// Run from repo root: go run .claude/scripts/extract-call-graph.go [./...]
package main

import (
	"encoding/json"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

type Edge struct {
	Caller     string `json:"caller"`
	CallerPkg  string `json:"caller_pkg"`
	Callee     string `json:"callee"`
	CalleePkg  string `json:"callee_pkg"`
	CallerFile string `json:"caller_file,omitempty"`
	CallerLine int    `json:"caller_line,omitempty"`
}

type FunctionNode struct {
	Name       string   `json:"name"`
	Package    string   `json:"package"`
	File       string   `json:"file,omitempty"`
	Line       int      `json:"line,omitempty"`
	Callers    int      `json:"callers"`
	Callees    int      `json:"callees"`
	CalleeList []string `json:"callees_list,omitempty"`
}

type Output struct {
	Generated   string                  `json:"generated"`
	Module      string                  `json:"module"`
	TotalEdges  int                     `json:"total_edges"`
	TotalFuncs  int                     `json:"total_functions"`
	Edges       []Edge                  `json:"edges"`
	EntryPoints []FunctionNode          `json:"entry_points"`
	MostCalled  []FunctionNode          `json:"most_called"`
	DeadCode    []FunctionNode          `json:"dead_code"`
	PackageStats map[string]PackageStat `json:"package_stats"`
}

type PackageStat struct {
	Functions    int `json:"functions"`
	InternalEdges int `json:"internal_edges"`
	IncomingEdges int `json:"incoming_edges"`
	OutgoingEdges int `json:"outgoing_edges"`
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
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedDeps,
		Dir:   repoRoot,
		Tests: false,
	}

	initial, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading packages: %v\n", err)
		os.Exit(1)
	}

	// Check for package load errors
	var loadErrors []string
	packages.Visit(initial, nil, func(pkg *packages.Package) {
		for _, err := range pkg.Errors {
			loadErrors = append(loadErrors, fmt.Sprintf("%s: %s", pkg.PkgPath, err.Msg))
		}
	})
	if len(loadErrors) > 10 {
		fmt.Fprintf(os.Stderr, "warning: %d package load errors (showing first 10)\n", len(loadErrors))
		for _, e := range loadErrors[:10] {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}

	modulePath := inferModulePath(initial)

	// Build SSA
	prog, ssaPkgs := ssautil.AllPackages(initial, ssa.InstantiateGenerics)
	prog.Build()

	// Filter to only our module's packages for VTA entry
	moduleFuncs := make(map[*ssa.Function]bool)
	for _, pkg := range ssaPkgs {
		if pkg == nil {
			continue
		}
		if !strings.HasPrefix(pkg.Pkg.Path(), modulePath) {
			continue
		}
		for _, member := range pkg.Members {
			if fn, ok := member.(*ssa.Function); ok {
				moduleFuncs[fn] = true
			}
		}
		// Also grab all functions including anonymous and methods
		for _, fn := range allFunctions(pkg) {
			moduleFuncs[fn] = true
		}
	}

	// Build call graph using VTA (pass nil for initial graph)
	cg := vta.CallGraph(moduleFuncs, nil)

	// Collect edges (only internal-to-module edges)
	var edges []Edge
	callerCount := make(map[string]int)  // callee -> number of callers
	calleeCount := make(map[string]int)  // caller -> number of callees
	funcFiles := make(map[string]string)
	funcLines := make(map[string]int)
	pkgStats := make(map[string]*PackageStat)

	ensurePkgStat := func(pkg string) *PackageStat {
		if _, ok := pkgStats[pkg]; !ok {
			pkgStats[pkg] = &PackageStat{}
		}
		return pkgStats[pkg]
	}

	seen := make(map[string]bool)

	cg.DeleteSyntheticNodes()

	err = callgraph.GraphVisitEdges(cg, func(edge *callgraph.Edge) error {
		callerFn := edge.Caller.Func
		calleeFn := edge.Callee.Func

		if callerFn == nil || calleeFn == nil {
			return nil
		}

		callerPkg := ""
		calleePkg := ""
		if callerFn.Package() != nil {
			callerPkg = callerFn.Package().Pkg.Path()
		}
		if calleeFn.Package() != nil {
			calleePkg = calleeFn.Package().Pkg.Path()
		}

		// Only keep edges where at least one side is in our module
		if !strings.HasPrefix(callerPkg, modulePath) && !strings.HasPrefix(calleePkg, modulePath) {
			return nil
		}

		callerName := callerFn.String()
		calleeName := calleeFn.String()

		// Deduplicate
		edgeKey := callerName + " -> " + calleeName
		if seen[edgeKey] {
			return nil
		}
		seen[edgeKey] = true

		// Shorten to relative package paths
		shortCallerPkg := strings.TrimPrefix(callerPkg, modulePath+"/")
		shortCalleePkg := strings.TrimPrefix(calleePkg, modulePath+"/")
		shortCaller := shortenFuncName(callerName, modulePath)
		shortCallee := shortenFuncName(calleeName, modulePath)

		// Get position info
		callerFile, callerLine := funcPos(callerFn, repoRoot)

		edge_ := Edge{
			Caller:     shortCaller,
			CallerPkg:  shortCallerPkg,
			Callee:     shortCallee,
			CalleePkg:  shortCalleePkg,
			CallerFile: callerFile,
			CallerLine: callerLine,
		}
		edges = append(edges, edge_)

		callerCount[shortCallee]++
		calleeCount[shortCaller]++

		// Track file/line
		if callerFile != "" {
			funcFiles[shortCaller] = callerFile
			funcLines[shortCaller] = callerLine
		}
		if f, l := funcPos(calleeFn, repoRoot); f != "" {
			funcFiles[shortCallee] = f
			funcLines[shortCallee] = l
		}

		// Package stats
		if strings.HasPrefix(callerPkg, modulePath) && strings.HasPrefix(calleePkg, modulePath) {
			if callerPkg == calleePkg {
				ensurePkgStat(shortCallerPkg).InternalEdges++
			} else {
				ensurePkgStat(shortCallerPkg).OutgoingEdges++
				ensurePkgStat(shortCalleePkg).IncomingEdges++
			}
		}

		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error visiting call graph: %v\n", err)
	}

	// Sort edges
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].CallerPkg != edges[j].CallerPkg {
			return edges[i].CallerPkg < edges[j].CallerPkg
		}
		return edges[i].Caller < edges[j].Caller
	})

	// Find entry points (functions with no callers that are exported)
	allFuncsSet := make(map[string]bool)
	for _, e := range edges {
		allFuncsSet[e.Caller] = true
		allFuncsSet[e.Callee] = true
	}
	var entryPoints []FunctionNode
	for fn := range allFuncsSet {
		if callerCount[fn] == 0 && calleeCount[fn] > 0 {
			entryPoints = append(entryPoints, FunctionNode{
				Name:    fn,
				Package: funcPkgFromEdges(fn, edges),
				File:    funcFiles[fn],
				Line:    funcLines[fn],
				Callers: 0,
				Callees: calleeCount[fn],
			})
		}
	}
	sort.Slice(entryPoints, func(i, j int) bool {
		return entryPoints[i].Callees > entryPoints[j].Callees
	})
	if len(entryPoints) > 30 {
		entryPoints = entryPoints[:30]
	}

	// Most called functions
	type kv struct {
		fn    string
		count int
	}
	var mostCalledList []kv
	for fn, count := range callerCount {
		mostCalledList = append(mostCalledList, kv{fn, count})
	}
	sort.Slice(mostCalledList, func(i, j int) bool {
		return mostCalledList[i].count > mostCalledList[j].count
	})
	var mostCalled []FunctionNode
	for i, mc := range mostCalledList {
		if i >= 30 {
			break
		}
		mostCalled = append(mostCalled, FunctionNode{
			Name:    mc.fn,
			Package: funcPkgFromEdges(mc.fn, edges),
			File:    funcFiles[mc.fn],
			Line:    funcLines[mc.fn],
			Callers: mc.count,
			Callees: calleeCount[mc.fn],
		})
	}

	// Dead code: functions defined but never called (no callers, no callees as caller)
	var deadCode []FunctionNode
	for fn := range allFuncsSet {
		if callerCount[fn] == 0 && calleeCount[fn] == 0 {
			deadCode = append(deadCode, FunctionNode{
				Name:    fn,
				Package: funcPkgFromEdges(fn, edges),
				File:    funcFiles[fn],
				Line:    funcLines[fn],
			})
		}
	}
	sort.Slice(deadCode, func(i, j int) bool {
		return deadCode[i].Name < deadCode[j].Name
	})
	if len(deadCode) > 50 {
		deadCode = deadCode[:50]
	}

	// Convert pkgStats
	pkgStatsOut := make(map[string]PackageStat)
	for k, v := range pkgStats {
		pkgStatsOut[k] = *v
	}

	out := Output{
		Generated:    time.Now().UTC().Format(time.RFC3339),
		Module:       modulePath,
		TotalEdges:   len(edges),
		TotalFuncs:   len(allFuncsSet),
		Edges:        edges,
		EntryPoints:  entryPoints,
		MostCalled:   mostCalled,
		DeadCode:     deadCode,
		PackageStats: pkgStatsOut,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

func allFunctions(pkg *ssa.Package) []*ssa.Function {
	var fns []*ssa.Function
	for _, member := range pkg.Members {
		switch m := member.(type) {
		case *ssa.Function:
			fns = append(fns, m)
			// Add anonymous functions within
			for _, anon := range m.AnonFuncs {
				fns = append(fns, anon)
			}
		case *ssa.Type:
			// Methods on named types (handle both Named and Alias types in Go 1.23+)
			named, ok := m.Type().(*types.Named)
			if !ok {
				mset := types.NewMethodSet(m.Type())
				for i := 0; i < mset.Len(); i++ {
					if fn, ok := mset.At(i).Obj().(*types.Func); ok {
						if method := prog_lookup(pkg.Prog, fn); method != nil {
							fns = append(fns, method)
						}
					}
				}
				break
			}
			for i := 0; i < named.NumMethods(); i++ {
				method := prog_lookup(pkg.Prog, named.Method(i))
				if method != nil {
					fns = append(fns, method)
				}
			}
			// Methods on pointer to named types
			ptr := types.NewPointer(named)
			mset := types.NewMethodSet(ptr)
			for i := 0; i < mset.Len(); i++ {
				sel := mset.At(i)
				if fn, ok := sel.Obj().(*types.Func); ok {
					if method := prog_lookup(pkg.Prog, fn); method != nil {
						fns = append(fns, method)
					}
				}
			}
		}
	}
	return fns
}

func prog_lookup(prog *ssa.Program, fn *types.Func) *ssa.Function {
	return prog.FuncValue(fn)
}

func shortenFuncName(name, modulePath string) string {
	return strings.ReplaceAll(name, "("+modulePath+"/", "(")
}

func funcPos(fn *ssa.Function, repoRoot string) (string, int) {
	if fn.Pos() == token.NoPos {
		return "", 0
	}
	pos := fn.Prog.Fset.Position(fn.Pos())
	return relPath(repoRoot, pos.Filename), pos.Line
}

func funcPkgFromEdges(fn string, edges []Edge) string {
	for _, e := range edges {
		if e.Caller == fn {
			return e.CallerPkg
		}
		if e.Callee == fn {
			return e.CalleePkg
		}
	}
	return ""
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
