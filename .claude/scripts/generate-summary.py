#!/usr/bin/env python3
"""generate-summary.py — Creates a compact summary index from full computed artifacts.

The summary is small enough (~2-4K tokens) for Claude to load on demand,
with pointers to full artifacts when detail is needed.
"""
import json
import os
import sys
from datetime import datetime, timezone

def main():
    computed_dir = sys.argv[1] if len(sys.argv) > 1 else ".claude/knowledge/computed"

    summary = {
        "generated": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "description": "Compact index of oauth-mcp-proxy computed knowledge artifacts. Load full artifacts for detail.",
    }

    # Summarize type-index: just package names + counts
    type_index_path = os.path.join(computed_dir, "type-index.json")
    if os.path.exists(type_index_path):
        with open(type_index_path) as f:
            data = json.load(f)
        pkg_summary = []
        for pkg in data.get("packages") or []:
            path = pkg["path"]
            short = path.split("/oauth-mcp-proxy/")[-1] if "/oauth-mcp-proxy/" in path else path
            pkg_summary.append({
                "pkg": short,
                "structs": len(pkg.get("structs") or []),
                "interfaces": len(pkg.get("interfaces") or []),
                "functions": len(pkg.get("functions") or []),
            })
        # Sort by total symbols descending
        pkg_summary.sort(key=lambda x: x["structs"] + x["interfaces"] + x["functions"], reverse=True)
        summary["type_index"] = {
            "total_types": data.get("total_types", 0),
            "total_functions": data.get("total_functions", 0),
            "total_packages": len(pkg_summary),
            "top_packages": pkg_summary[:30],
            "full_artifact": "type-index.json",
        }

    # Summarize interface-map: interfaces with most implementors
    iface_path = os.path.join(computed_dir, "interface-map.json")
    if os.path.exists(iface_path):
        with open(iface_path) as f:
            data = json.load(f)
        ifaces = data.get("interfaces") or []
        iface_summary = []
        for iface in ifaces:
            impls = iface.get("implementors") or []
            short_pkg = iface["package"].split("/oauth-mcp-proxy/")[-1] if "/oauth-mcp-proxy/" in iface["package"] else iface["package"]
            iface_summary.append({
                "name": iface["name"],
                "package": short_pkg,
                "methods": len(iface.get("methods", [])),
                "implementors": len(impls),
            })
        iface_summary.sort(key=lambda x: x["implementors"], reverse=True)
        summary["interface_map"] = {
            "total_interfaces": len(ifaces),
            "top_by_implementors": iface_summary[:20],
            "full_artifact": "interface-map.json",
        }

    # Summarize internal-deps: most-imported and most-importing packages
    deps_path = os.path.join(computed_dir, "internal-deps.json")
    if os.path.exists(deps_path):
        with open(deps_path) as f:
            data = json.load(f)
        pkgs = data.get("packages", {})

        most_imported = sorted(
            pkgs.items(),
            key=lambda x: len(x[1].get("imported_by") or []),
            reverse=True
        )[:15]

        most_importing = sorted(
            pkgs.items(),
            key=lambda x: len(x[1].get("imports") or []),
            reverse=True
        )[:15]

        summary["internal_deps"] = {
            "total_packages": data.get("total_packages", len(pkgs)),
            "most_depended_on": [
                {"pkg": k, "imported_by_count": len(v.get("imported_by") or [])}
                for k, v in most_imported
            ],
            "most_dependencies": [
                {"pkg": k, "imports_count": len(v.get("imports") or [])}
                for k, v in most_importing
            ],
            "full_artifact": "internal-deps.json",
        }

    # Summarize git intelligence: top churn files and hotspots
    git_path = os.path.join(computed_dir, "git-intelligence.json")
    if os.path.exists(git_path):
        with open(git_path) as f:
            data = json.load(f)
        summary["git_intelligence"] = {
            "period_days": data.get("period_days"),
            "top_churn_files": data.get("file_churn", [])[:15],
            "top_bug_hotspots": data.get("bug_hotspots", [])[:10],
            "full_artifact": "git-intelligence.json",
        }

    # API surface is already small, just reference it
    api_path = os.path.join(computed_dir, "api-surface.json")
    if os.path.exists(api_path):
        summary["api_surface"] = {
            "note": "API surface artifact is small enough to load directly.",
            "full_artifact": "api-surface.json",
        }

    # Summarize concurrency map: top packages by concurrency usage
    conc_path = os.path.join(computed_dir, "concurrency-map.json")
    if os.path.exists(conc_path):
        with open(conc_path) as f:
            data = json.load(f)
        s = data.get("summary", {})
        pkg_summ = data.get("package_summary") or []
        summary["concurrency_map"] = {
            "total_goroutines": s.get("total_goroutines", 0),
            "total_channels": s.get("total_channels", 0),
            "total_mutexes": s.get("total_mutexes", 0),
            "total_contexts": s.get("total_contexts", 0),
            "packages_with_concurrency": s.get("packages_with_concurrency", 0),
            "top_packages": pkg_summ[:15],
            "full_artifact": "concurrency-map.json",
        }

    # Summarize call graph: entry points, most called, package stats
    cg_path = os.path.join(computed_dir, "call-graph.json")
    if os.path.exists(cg_path):
        with open(cg_path) as f:
            data = json.load(f)
        most_called = data.get("most_called") or []
        pkg_stats = data.get("package_stats") or {}
        top_pkgs = sorted(pkg_stats.items(), key=lambda x: x[1].get("internal_edges", 0), reverse=True)[:15]
        summary["call_graph"] = {
            "total_edges": data.get("total_edges", 0),
            "total_functions": data.get("total_functions", 0),
            "entry_points": len(data.get("entry_points") or []),
            "most_called_top10": [{"name": f["name"][:80], "callers": f["callers"]} for f in most_called[:10]],
            "top_packages_by_internal_edges": [
                {"pkg": k, "internal": v.get("internal_edges", 0), "incoming": v.get("incoming_edges", 0), "outgoing": v.get("outgoing_edges", 0)}
                for k, v in top_pkgs
            ],
            "full_artifact": "call-graph.json",
        }

    json.dump(summary, sys.stdout, indent=2)
    print()


if __name__ == "__main__":
    main()
