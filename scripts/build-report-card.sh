#!/bin/bash
# Generate the op-forward quality and security report card.
#
# Usage: scripts/build-report-card.sh [--gate] <output-dir>
#
# Runs formatting, static analysis, security, vulnerability and test checks
# against the repository, grades each category, and writes:
#
#   <output-dir>/index.html   self-contained report (no external CSS or JS)
#   <output-dir>/badge.json   shields.io endpoint payload for the README badge
#
# Required tools on PATH: go, gofmt, golangci-lint, gosec, govulncheck.
# No tool versions are embedded here; the CI workflow pins them.
#
# Exit status:
#   0   the report was generated (findings never change this by themselves)
#   1   report generation failed (missing tool, tool crash, unwritable output)
#   2   --gate was given and a blocking condition was found: a failing test
#       or a govulncheck vulnerability that affects this module
#
# The same script therefore serves reporting and gating: run it plainly to
# publish the report, and again with --gate to fail a build on blocking
# conditions only. Lint and gosec findings lower the grade but never block.
#
# GRADING
#
# Every category starts at 100 points and loses points as follows:
#
#   Category       Deduction
#   -------------  ---------------------------------------------------------
#   gofmt          10 per file reported by gofmt -l
#   go vet         10 per finding
#   golangci-lint  5 per finding
#   gosec          30 per HIGH, 10 per MEDIUM, 2 per LOW finding
#   govulncheck    100 if any vulnerability affects this module (reachable
#                  code), otherwise 0; unreachable advisories are not counted
#   go test        100 if any test or package build fails; otherwise
#                  scaled by statement coverage: score = min(100, coverage
#                  percent * 100 / 80), so 80 percent coverage or more earns
#                  full marks
#
# A category score never drops below 0. Scores map to letter grades:
#
#   Score     Grade
#   --------  -----
#   100       A+
#   90 - 99   A
#   80 - 89   B
#   70 - 79   C
#   60 - 69   D
#   0 - 59    F
#
# The overall score is the integer average of the six category scores (equal
# weight) and maps through the same table. Regardless of the average, the
# overall grade is capped at F when any test fails or when govulncheck
# reports a vulnerability that affects this module.

set -euo pipefail

GATE=false
OUT_ARG=""
for arg in "$@"; do
    case "$arg" in
        --gate) GATE=true ;;
        -*)     echo "Unknown option: $arg" >&2; exit 1 ;;
        *)      OUT_ARG="$arg" ;;
    esac
done
if [ -z "$OUT_ARG" ]; then
    echo "Usage: build-report-card.sh [--gate] <output-dir>" >&2
    exit 1
fi

die() {
    echo "build-report-card: $*" >&2
    exit 1
}

# Resolve the output directory before changing into the repository root so a
# relative path means what the caller expects.
mkdir -p "$OUT_ARG" || die "cannot create output directory $OUT_ARG"
OUT_DIR="$(cd "$OUT_ARG" && pwd)"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE/.."

require_tool() {
    command -v "$1" >/dev/null 2>&1 || die "$1 not found on PATH. Install: $2"
}
require_tool go "https://go.dev/doc/install"
require_tool gofmt "https://go.dev/doc/install"
require_tool golangci-lint "https://golangci-lint.run/welcome/install/"
require_tool gosec "https://github.com/securego/gosec#install"
require_tool govulncheck "go install golang.org/x/vuln/cmd/govulncheck@latest"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# --- helpers -----------------------------------------------------------------

# Count the lines in a file; prints 0 for an empty or missing file.
count_lines() {
    if [ -s "$1" ]; then wc -l < "$1" | tr -d ' '; else echo 0; fi
}

# Count regular-expression matches (lines) in a file; prints 0 when none.
count_matches() {
    grep -Ec "$1" "$2" 2>/dev/null || true
}

# Map a 0-100 score to a letter grade per the table in the header.
grade_for() {
    local s=$1
    if   [ "$s" -ge 100 ]; then echo "A+"
    elif [ "$s" -ge 90 ];  then echo "A"
    elif [ "$s" -ge 80 ];  then echo "B"
    elif [ "$s" -ge 70 ];  then echo "C"
    elif [ "$s" -ge 60 ];  then echo "D"
    else echo "F"
    fi
}

# Subtract a deduction from 100, flooring at 0.
deduct() {
    local d=$1
    if [ "$d" -gt 100 ]; then d=100; fi
    echo $((100 - d))
}

# Escape text for inclusion in HTML.
html_escape() {
    sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

# Emit a collapsible-free, plain <pre> block with the first N lines of a file.
detail_block() {
    local title=$1 file=$2 max=${3:-40}
    [ -s "$file" ] || return 0
    printf '<h3>%s</h3>\n<pre>\n' "$title"
    head -n "$max" "$file" | html_escape
    if [ "$(count_lines "$file")" -gt "$max" ]; then
        printf '... (%s more lines omitted)\n' "$(($(count_lines "$file") - max))"
    fi
    printf '</pre>\n'
}

# --- checks ------------------------------------------------------------------

# gofmt: files whose formatting differs from the canonical style. Only files
# tracked by git are scanned, so a local run is not polluted by build output
# or by other worktrees under .agent-grid. A syntax error makes gofmt exit
# nonzero without listing the file, so it is counted as one finding rather
# than silently passing.
gofmt_rc=0
git ls-files -z -- '*.go' | xargs -0 gofmt -l > "$WORK/gofmt.txt" 2> "$WORK/gofmt.err" || gofmt_rc=$?
gofmt_count=$(count_lines "$WORK/gofmt.txt")
if [ "$gofmt_rc" -ne 0 ] && [ "$gofmt_count" -eq 0 ]; then
    gofmt_count=1
    cat "$WORK/gofmt.err" > "$WORK/gofmt.txt"
fi

# go vet: finding lines carry a file:line:col position; "#" lines only name
# the package being analyzed.
vet_rc=0
go vet ./... > "$WORK/vet.out" 2>&1 || vet_rc=$?
grep -E '^[^#[:space:]].*:[0-9]+:[0-9]+:' "$WORK/vet.out" > "$WORK/vet.txt" || true
vet_count=$(count_lines "$WORK/vet.txt")
if [ "$vet_rc" -ne 0 ] && [ "$vet_count" -eq 0 ]; then
    vet_count=1
    cp "$WORK/vet.out" "$WORK/vet.txt"
fi

# golangci-lint: exit 1 means issues were found; any other nonzero status is
# an operational failure (bad configuration, crash) and aborts generation.
lint_rc=0
golangci-lint run ./... > "$WORK/lint.out" 2> "$WORK/lint.err" || lint_rc=$?
if [ "$lint_rc" -gt 1 ]; then
    cat "$WORK/lint.err" >&2
    die "golangci-lint failed to run (exit $lint_rc)"
fi
grep -E '^[^[:space:]].*:[0-9]+:[0-9]+: ' "$WORK/lint.out" > "$WORK/lint.txt" || true
lint_count=$(count_lines "$WORK/lint.txt")

# gosec: -no-fail keeps the exit status at zero when issues exist, so any
# nonzero status is an operational failure. Severities are counted from the
# JSON report.
gosec -fmt=json -quiet -no-fail -out="$WORK/gosec.json" ./... > /dev/null 2> "$WORK/gosec.err" \
    || { cat "$WORK/gosec.err" >&2; die "gosec failed to run"; }
[ -f "$WORK/gosec.json" ] || die "gosec produced no report"
gosec_high=$(count_matches '"severity": *"HIGH"' "$WORK/gosec.json")
gosec_medium=$(count_matches '"severity": *"MEDIUM"' "$WORK/gosec.json")
gosec_low=$(count_matches '"severity": *"LOW"' "$WORK/gosec.json")
gosec_total=$((gosec_high + gosec_medium + gosec_low))

# govulncheck: exit 0 means nothing affects this module and exit 3 means at
# least one reachable vulnerability was found. Text mode prints a numbered
# "Vulnerability #N" entry only for affecting vulnerabilities; advisories for
# modules or packages that are required but never called are summarized
# separately and intentionally not counted.
vuln_rc=0
govulncheck ./... > "$WORK/vuln.out" 2> "$WORK/vuln.err" || vuln_rc=$?
if [ "$vuln_rc" -ne 0 ] && [ "$vuln_rc" -ne 3 ]; then
    cat "$WORK/vuln.err" >&2
    die "govulncheck failed to run (exit $vuln_rc)"
fi
vuln_count=$(count_matches '^Vulnerability #[0-9]+:' "$WORK/vuln.out")
if [ "$vuln_rc" -eq 3 ] && [ "$vuln_count" -eq 0 ]; then
    vuln_count=1
fi
grep -E '^(Vulnerability #|    Found in:|    Fixed in:)' "$WORK/vuln.out" > "$WORK/vuln.txt" || true

# go test with statement coverage across every package. Tests failing is a
# normal outcome to report, not a generation failure.
test_rc=0
go test -coverprofile="$WORK/cover.out" ./... > "$WORK/test.out" 2>&1 || test_rc=$?
coverage="0.0"
if [ -s "$WORK/cover.out" ]; then
    coverage=$(go tool cover -func="$WORK/cover.out" 2>/dev/null \
        | awk '/^total:/ { gsub("%", "", $3); print $3 }')
    coverage=${coverage:-0.0}
fi
grep -E '^(--- FAIL|FAIL|panic:)' "$WORK/test.out" > "$WORK/test.txt" || true

# --- grading -----------------------------------------------------------------

gofmt_score=$(deduct $((gofmt_count * 10)))
vet_score=$(deduct $((vet_count * 10)))
lint_score=$(deduct $((lint_count * 5)))
gosec_score=$(deduct $((gosec_high * 30 + gosec_medium * 10 + gosec_low * 2)))

if [ "$vuln_count" -gt 0 ]; then vuln_score=0; else vuln_score=100; fi

if [ "$test_rc" -ne 0 ]; then
    test_score=0
else
    test_score=$(awk -v c="$coverage" 'BEGIN { s = int(c * 100 / 80); if (s > 100) s = 100; print s }')
fi

overall_score=$(( (gofmt_score + vet_score + lint_score + gosec_score + vuln_score + test_score) / 6 ))
overall_grade=$(grade_for "$overall_score")
capped=false
if [ "$test_rc" -ne 0 ] || [ "$vuln_count" -gt 0 ]; then
    overall_grade="F"
    capped=true
fi

case "$overall_grade" in
    A+|A) color="brightgreen" ;;
    B)    color="green" ;;
    C)    color="yellow" ;;
    D)    color="orange" ;;
    *)    color="red" ;;
esac

# --- tool versions -------------------------------------------------------------

version_of() {
    "$@" 2>&1 | head -n 3 | tr '\n' ' ' | sed -e 's/ *$//' | html_escape
}
go_version=$(go version | html_escape)
lint_version=$(version_of golangci-lint --version)
gosec_version=$(version_of gosec -version)
vuln_version=$(version_of govulncheck -version)
generated_at=$(date -u +"%Y-%m-%d %H:%M:%S UTC")

# --- report --------------------------------------------------------------------

if [ "$test_rc" -eq 0 ]; then test_result="pass"; else test_result="FAIL"; fi
if [ "$gofmt_count" -eq 0 ]; then gofmt_detail="all files formatted"; else gofmt_detail="$gofmt_count file(s) need formatting"; fi

row() {
    printf '<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n' "$1" "$2" "$3" "$4"
}

{
    cat <<HTMLEOF
<!DOCTYPE html>
<html><head><title>op-forward report card</title></head>
<body>
<h1>op-forward report card</h1>
<p>Overall grade: <strong>${overall_grade}</strong> (score ${overall_score} of 100).
   Generated ${generated_at}. Source:
   <a href="https://github.com/ekovshilovsky/op-forward">github.com/ekovshilovsky/op-forward</a></p>
HTMLEOF
    if [ "$capped" = true ]; then
        echo '<p>The overall grade is capped at F because a test failed or a vulnerability affects this module.</p>'
    fi
    cat <<HTMLEOF
<h2>Results</h2>
<table border="1" cellpadding="4" cellspacing="0">
<tr><th>Category</th><th>Result</th><th>Score</th><th>Grade</th></tr>
HTMLEOF
    row "gofmt" "$gofmt_detail" "$gofmt_score" "$(grade_for "$gofmt_score")"
    row "go vet" "$vet_count finding(s)" "$vet_score" "$(grade_for "$vet_score")"
    row "golangci-lint" "$lint_count finding(s)" "$lint_score" "$(grade_for "$lint_score")"
    row "gosec" "$gosec_high high, $gosec_medium medium, $gosec_low low" "$gosec_score" "$(grade_for "$gosec_score")"
    row "govulncheck" "$vuln_count affecting vulnerability(ies)" "$vuln_score" "$(grade_for "$vuln_score")"
    row "go test" "$test_result, ${coverage}% statement coverage" "$test_score" "$(grade_for "$test_score")"
    cat <<HTMLEOF
</table>
<h2>Grading</h2>
<p>Each category starts at 100 points. The overall score is the integer average of the six categories.
   The overall grade is F whenever a test fails or a vulnerability affects this module.</p>
<table border="1" cellpadding="4" cellspacing="0">
<tr><th>Category</th><th>Deduction</th></tr>
<tr><td>gofmt</td><td>10 per unformatted file</td></tr>
<tr><td>go vet</td><td>10 per finding</td></tr>
<tr><td>golangci-lint</td><td>5 per finding</td></tr>
<tr><td>gosec</td><td>30 per high, 10 per medium, 2 per low finding</td></tr>
<tr><td>govulncheck</td><td>100 if any vulnerability affects this module (reachable code); unreachable advisories are not counted</td></tr>
<tr><td>go test</td><td>100 if any test fails; otherwise score is coverage percent times 100 divided by 80, capped at 100</td></tr>
</table>
<table border="1" cellpadding="4" cellspacing="0">
<tr><th>Score</th><th>Grade</th></tr>
<tr><td>100</td><td>A+</td></tr>
<tr><td>90 - 99</td><td>A</td></tr>
<tr><td>80 - 89</td><td>B</td></tr>
<tr><td>70 - 79</td><td>C</td></tr>
<tr><td>60 - 69</td><td>D</td></tr>
<tr><td>0 - 59</td><td>F</td></tr>
</table>
<h2>Tool versions</h2>
<pre>
${go_version}
golangci-lint: ${lint_version}
gosec: ${gosec_version}
govulncheck: ${vuln_version}
</pre>
HTMLEOF
    if [ -s "$WORK/gofmt.txt" ] || [ -s "$WORK/vet.txt" ] || [ -s "$WORK/lint.txt" ] \
        || [ -s "$WORK/vuln.txt" ] || [ -s "$WORK/test.txt" ]; then
        echo '<h2>Details</h2>'
        detail_block "gofmt" "$WORK/gofmt.txt"
        detail_block "go vet" "$WORK/vet.txt"
        detail_block "golangci-lint" "$WORK/lint.txt"
        detail_block "govulncheck" "$WORK/vuln.txt"
        detail_block "go test" "$WORK/test.txt"
    fi
    echo '</body></html>'
} > "$OUT_DIR/index.html" || die "cannot write $OUT_DIR/index.html"

printf '{"schemaVersion":1,"label":"report card","message":"%s","color":"%s"}\n' \
    "$overall_grade" "$color" > "$OUT_DIR/badge.json" || die "cannot write $OUT_DIR/badge.json"

echo "Report card: ${overall_grade} (score ${overall_score}); written to ${OUT_DIR}"

if [ "$GATE" = true ] && { [ "$test_rc" -ne 0 ] || [ "$vuln_count" -gt 0 ]; }; then
    echo "Gate failed: tests=${test_result}, affecting vulnerabilities=${vuln_count}" >&2
    exit 2
fi
exit 0
