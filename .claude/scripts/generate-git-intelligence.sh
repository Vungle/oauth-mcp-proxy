#!/bin/bash
# generate-git-intelligence.sh — Analyzes git history for churn, hotspots, and co-change coupling.
# Outputs JSON to stdout.
# Usage: .claude/scripts/generate-git-intelligence.sh [days=180]
set -euo pipefail

DAYS="${1:-180}"
SINCE="$(date -u -v-${DAYS}d +%Y-%m-%d 2>/dev/null || date -u -d "${DAYS} days ago" +%Y-%m-%d)"

# Ensure we're in a git repo
if ! git rev-parse --is-inside-work-tree &>/dev/null; then
  echo '{"error": "not a git repository"}' >&2
  exit 1
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

###############################################################################
# 1. File churn: commits + lines added/deleted per file
###############################################################################
git log --since="$SINCE" --numstat --format='%H' -- '*.go' \
  | awk '
    /^[0-9a-f]{40}$/ { commits[$0]=1; current=$0; next }
    NF==3 && $1 != "-" {
      file=$3
      added[file]+=$1
      deleted[file]+=$2
      if (!(file SUBSEP current in seen)) {
        seen[file, current]=1
        ncommits[file]++
      }
    }
    END {
      for (f in ncommits)
        printf "%s\t%d\t%d\t%d\n", f, ncommits[f], added[f], deleted[f]
    }
  ' | sort -t$'\t' -k2 -rn > "$TMPDIR/churn.tsv"

###############################################################################
# 2. Bug hotspots: files most frequently changed in commits mentioning fix/bug
###############################################################################
git log --since="$SINCE" --numstat --format='%H %s' -- '*.go' \
  | awk '
    /^[0-9a-f]{40} / {
      current=$1
      $1=""
      msg=tolower($0)
      is_fix = (msg ~ /fix|bug|hotfix|patch|revert|issue/)
      next
    }
    NF==3 && is_fix && $1 != "-" {
      file=$3
      if (!(file SUBSEP current in seen)) {
        seen[file, current]=1
        fixes[file]++
      }
    }
    END {
      for (f in fixes)
        printf "%s\t%d\n", f, fixes[f]
    }
  ' | sort -t$'\t' -k2 -rn > "$TMPDIR/hotspots.tsv"

###############################################################################
# 3. Co-change coupling: files frequently changed together
###############################################################################
git log --since="$SINCE" --name-only --pretty=format:'COMMIT_SEP' -- '*.go' \
  | awk '
    /^COMMIT_SEP$/ { if (n>0) { for(i=0;i<n;i++) for(j=i+1;j<n;j++) { pair=files[i] "\t" files[j]; count[pair]++ } } n=0; next }
    /^$/ { next }
    NF>0 { files[n++]=$0 }
    END {
      for (p in count) if (count[p]>=3)
        printf "%s\t%d\n", p, count[p]
    }
  ' | sort -t$'\t' -k3 -rn | head -50 > "$TMPDIR/cochange.tsv"

###############################################################################
# 4. Directory ownership: top committers per top-level directory
###############################################################################
git log --since="$SINCE" --format='%aN' --name-only -- '*.go' \
  | awk '
    /^$/ { next }
    !/:/ && NF>0 && prev=="" { author=$0; prev="author"; next }
    NF>0 && prev=="author" {
      split($0, parts, "/")
      dir=parts[1]
      if (parts[1]=="internal" || parts[1]=="pkg" || parts[1]=="cmd")
        dir=parts[1] "/" parts[2]
      key=dir "\t" author
      count[key]++
      prev=""
      next
    }
    { prev="" }
    END {
      for (k in count) printf "%s\t%d\n", k, count[k]
    }
  ' | sort -t$'\t' -k1,1 -k3 -rn > "$TMPDIR/ownership.tsv"

###############################################################################
# 5. Assemble JSON output
###############################################################################
python3 -c "
import json, sys, os
from datetime import datetime, timezone

tmpdir = sys.argv[1]
days = int(sys.argv[2])

def read_tsv(path, cols):
    rows = []
    if not os.path.exists(path):
        return rows
    with open(path) as f:
        for line in f:
            parts = line.rstrip('\n').split('\t')
            if len(parts) >= len(cols):
                row = {}
                for i, col in enumerate(cols):
                    row[col] = int(parts[i]) if cols[col] == 'int' else parts[i]
                rows.append(row)
    return rows

# File churn — top 50
churn = read_tsv(f'{tmpdir}/churn.tsv', {'file': 'str', 'commits': 'int', 'lines_added': 'int', 'lines_deleted': 'int'})[:50]

# Bug hotspots — top 30
hotspots = read_tsv(f'{tmpdir}/hotspots.tsv', {'file': 'str', 'fix_commits': 'int'})[:30]

# Co-change coupling — already limited to top 50
cochange_raw = read_tsv(f'{tmpdir}/cochange.tsv', {'file_a': 'str', 'file_b': 'str', 'times': 'int'})
cochange = [{'file_a': r['file_a'], 'file_b': r['file_b'], 'times': r['times']} for r in cochange_raw]

# Directory ownership — group by dir, top 3 authors each
ownership_raw = read_tsv(f'{tmpdir}/ownership.tsv', {'directory': 'str', 'author': 'str', 'commits': 'int'})
dir_owners = {}
for r in ownership_raw:
    d = r['directory']
    if d not in dir_owners:
        dir_owners[d] = []
    dir_owners[d].append({'author': r['author'], 'commits': r['commits']})
# Top 3 per dir
ownership = []
for d in sorted(dir_owners):
    top = sorted(dir_owners[d], key=lambda x: x['commits'], reverse=True)[:3]
    ownership.append({'directory': d, 'top_authors': top})

out = {
    'generated': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
    'period_days': days,
    'file_churn': churn,
    'bug_hotspots': hotspots,
    'co_change_coupling': cochange,
    'directory_ownership': ownership,
}

json.dump(out, sys.stdout, indent=2)
print()
" "$TMPDIR" "$DAYS"
