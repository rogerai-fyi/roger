#!/bin/sh
# scripts/smoke.sh - RogerAI release-validation gate.
#
# A repeatable SMOKE / integration test that runs the same checks a release must
# pass, then prints a single PASS/FAIL summary. Exits non-zero on ANY failure so
# it can gate a `git tag` (see CLAUDE.md "Release gate").
#
# Sections:
#   BUILD      go build + go vet + gofmt cleanliness + the web build (build.mjs)
#   UNIT       go test ./...  (the regression suite, per-package pass/fail)
#   WEB ROUTES serve web/dist locally, assert every page returns 200, then crawl
#              every internal <a href> in dist and assert each resolves (catches
#              the clean-URL-404 class of bug: a link to /foo when only /foo.html
#              exists on a host that serves NO clean URLs).
#   LIVE       (opt-in, --live) curl the production site + broker /health, and a
#              credentialed-CORS preflight check (Access-Control-Allow-Origin must
#              ECHO the web origin, never "*").
#
# Usage:
#   scripts/smoke.sh            # BUILD + UNIT + WEB ROUTES (offline, hermetic)
#   scripts/smoke.sh --live     # also hit production (network required)
#   make smoke / make smoke-live
#
# POSIX sh. No bashisms. No em/en dashes anywhere in output.
set -u

# --- locate repo root (this script lives in scripts/) -----------------------
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
cd "$ROOT" || { echo "cannot cd to repo root: $ROOT"; exit 2; }

GOTOOLCHAIN=local
export GOTOOLCHAIN

LIVE=0
for arg in "$@"; do
	case "$arg" in
		--live) LIVE=1 ;;
		-h|--help)
			sed -n '2,30p' "$0"
			exit 0
			;;
		*) echo "unknown flag: $arg (use --live)"; exit 2 ;;
	esac
done

# Production targets for --live.
SITE_BASE=${ROGERAI_SITE_BASE:-https://rogerai.fyi}
BROKER_BASE=${ROGERAI_BROKER_BASE:-https://broker.rogerai.fyi}
WEB_ORIGIN=${ROGERAI_WEB_ORIGIN:-https://rogerai.fyi}

# --- pass/fail bookkeeping ---------------------------------------------------
FAILS=0
PASSES=0
FAILED_NAMES=""

green() { printf '  PASS  %s\n' "$1"; PASSES=$((PASSES + 1)); }
red() {
	printf '  FAIL  %s\n' "$1"
	FAILS=$((FAILS + 1))
	FAILED_NAMES="$FAILED_NAMES
    - $1"
}
info() { printf '        %s\n' "$1"; }
section() { printf '\n== %s ==\n' "$1"; }

# run a command, capture output to a temp log, PASS/FAIL on exit status.
run() {
	label=$1
	shift
	log=$(mktemp)
	if "$@" >"$log" 2>&1; then
		green "$label"
		rm -f "$log"
	else
		red "$label"
		# show the tail of the failure so the gate is actionable.
		sed 's/^/        | /' "$log" | tail -n 20
		rm -f "$log"
	fi
}

# --- cleanup -----------------------------------------------------------------
HTTP_PID=""
cleanup() {
	[ -n "$HTTP_PID" ] && kill "$HTTP_PID" 2>/dev/null
}
trap cleanup EXIT INT TERM

printf 'RogerAI smoke gate  (root: %s)\n' "$ROOT"
[ "$LIVE" -eq 1 ] && printf 'mode: BUILD + UNIT + WEB + LIVE\n' || printf 'mode: BUILD + UNIT + WEB  (pass --live to also hit prod)\n'

# ============================================================================
# BUILD
# ============================================================================
section "BUILD"

run "go build ./..." go build ./...
run "go vet ./..." go vet ./...

# gofmt -l prints files that need formatting; any output is a failure.
gofmt_out=$(gofmt -l cmd internal 2>&1)
if [ -z "$gofmt_out" ]; then
	green "gofmt clean (cmd internal)"
else
	red "gofmt clean (cmd internal)"
	printf '%s\n' "$gofmt_out" | sed 's/^/        | needs fmt: /'
fi

# Web build: node web/build.mjs must succeed and emit web/dist.
if command -v node >/dev/null 2>&1; then
	run "web build (node web/build.mjs)" node web/build.mjs
else
	red "web build (node web/build.mjs)"
	info "node not found on PATH"
fi

# Manual version sync: the built operating manual MUST mention the current CLI
# version. This is the sync guard - any release that bumps the `Version` fallback in
# cmd/rogerai/main.go but forgets to update web/src/manual.html (cover + changelog)
# fails the gate here. Lightest reliable check: grep the version out of the source
# of truth, then require it in the built dist manual. See CLAUDE.md "Release gate".
# Matches both `const Version = "..."` and `var Version = "..."` (the fallback became
# a var so a release/beta can stamp a semver via -ldflags -X main.Version).
cli_ver=$(sed -n 's/^\(const\|var\) Version = "\([0-9][^"]*\)".*/\2/p' cmd/rogerai/main.go | head -n 1)
if [ -z "$cli_ver" ]; then
	red "read Version from cmd/rogerai/main.go"
elif [ ! -f "$ROOT/web/dist/manual.html" ]; then
	red "manual mentions CLI version v$cli_ver (web/dist/manual.html missing - run node web/build.mjs)"
elif grep -q "$cli_ver" "$ROOT/web/dist/manual.html"; then
	green "manual mentions current CLI version (v$cli_ver)"
else
	red "manual mentions current CLI version (v$cli_ver)"
	info "web/dist/manual.html does not contain '$cli_ver' - update web/src/manual.html (cover + changelog) and re-run node web/build.mjs"
fi

# ============================================================================
# UNIT  (the regression suite, per-package)
# ============================================================================
section "UNIT"

# go test ./... with -v-free per-package lines: "ok pkg", "FAIL pkg", "? pkg [no test files]".
test_log=$(mktemp)
go test ./... >"$test_log" 2>&1
test_rc=$?
while IFS= read -r line; do
	case "$line" in
		ok\ *|ok'	'*)   green "test $(printf '%s' "$line" | awk '{print $2}')" ;;
		FAIL\ *|FAIL'	'*) red "test $(printf '%s' "$line" | awk '{print $2}')" ;;
		\?\ *|\?'	'*)   info "skip $(printf '%s' "$line" | awk '{print $2}') (no test files)" ;;
		*)               : ;; # build errors / panics fall through; surfaced below
	esac
done <"$test_log"
if [ "$test_rc" -ne 0 ]; then
	# Surface anything that was not a clean per-package line (compile errors, panics).
	non_pkg=$(grep -vE '^(ok|FAIL|\?)[ 	]' "$test_log" | grep -E 'panic|cannot|error|undefined|FAIL' | head -n 15)
	if [ -n "$non_pkg" ]; then
		red "go test ./... (build/run errors)"
		printf '%s\n' "$non_pkg" | sed 's/^/        | /'
	fi
fi
rm -f "$test_log"

# ============================================================================
# WEB ROUTES  (serve dist, assert 200s, crawl internal links)
# ============================================================================
section "WEB ROUTES"

DIST="$ROOT/web/dist"
if [ ! -d "$DIST" ]; then
	red "web/dist exists"
elif ! command -v python3 >/dev/null 2>&1; then
	red "python3 available (to serve web/dist)"
elif ! command -v curl >/dev/null 2>&1; then
	red "curl available (to probe web/dist)"
else
	# Serve dist on an ephemeral-ish port; retry a few ports in case of collision.
	PORT=0
	for p in 8771 8772 8773 8774 8775; do
		( cd "$DIST" && exec python3 -m http.server "$p" --bind 127.0.0.1 ) >/dev/null 2>&1 &
		HTTP_PID=$!
		# wait for the server to answer (up to ~3s).
		up=0
		i=0
		while [ "$i" -lt 30 ]; do
			if curl -fsS -o /dev/null "http://127.0.0.1:$p/index.html" 2>/dev/null; then
				up=1
				break
			fi
			# bail early if the server died (port in use).
			kill -0 "$HTTP_PID" 2>/dev/null || break
			sleep 0.1
			i=$((i + 1))
		done
		if [ "$up" -eq 1 ]; then
			PORT=$p
			break
		fi
		kill "$HTTP_PID" 2>/dev/null
		HTTP_PID=""
	done

	if [ "$PORT" -eq 0 ]; then
		red "serve web/dist (python3 -m http.server)"
	else
		green "serve web/dist on 127.0.0.1:$PORT"
		BASE="http://127.0.0.1:$PORT"

		# 1) Every built page returns 200.
		page_fail=0
		for f in "$DIST"/*.html; do
			name=$(basename "$f")
			code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$name")
			if [ "$code" = "200" ]; then
				:
			else
				red "page /$name -> $code (want 200)"
				page_fail=1
			fi
		done
		[ "$page_fail" -eq 0 ] && green "all pages return 200 ($(ls "$DIST"/*.html | wc -l | tr -d ' ') pages)"

		# 2) Crawl every internal <a href> and assert each resolves.
		#    Internal = starts with "/" or is a bare relative path. We map a href
		#    to a file under dist and require it to exist; pure "#anchor" and
		#    external "http(s)://" / "mailto:" links are skipped.
		broken=""
		nlinks=0
		# Extract hrefs from all pages, dedupe.
		hrefs=$(grep -rhoE 'href="[^"]+"' "$DIST"/*.html \
			| sed -e 's/^href="//' -e 's/"$//' \
			| sort -u)
		for href in $hrefs; do
			case "$href" in
				http://*|https://*|//*|mailto:*|tel:*|javascript:*) continue ;; # external
				\#*) continue ;;                                                 # in-page anchor
			esac
			# strip a #fragment and ?query for the file-existence check.
			path=${href%%#*}
			path=${path%%\?*}
			[ -z "$path" ] && continue
			nlinks=$((nlinks + 1))

			# resolve to a file under dist.
			case "$path" in
				/*) target="$DIST$path" ;;     # root-relative
				*)  target="$DIST/$path" ;;     # page-relative (all pages are top-level)
			esac

			# a trailing-slash or bare dir would need index.html; dist has none, so
			# require the exact file. A href ending in "/" is treated as dir/index.html.
			case "$target" in
				*/) target="${target}index.html" ;;
			esac

			if [ ! -f "$target" ]; then
				broken="$broken $href"
			fi
		done
		if [ -z "$broken" ]; then
			green "internal link crawl ($nlinks links resolve)"
		else
			red "internal link crawl (broken links)"
			for b in $broken; do info "broken: $b (clean-URL 404 risk)"; done
		fi

		kill "$HTTP_PID" 2>/dev/null
		HTTP_PID=""
	fi
fi

# ============================================================================
# LIVE  (opt-in)
# ============================================================================
if [ "$LIVE" -eq 1 ]; then
	section "LIVE (production)"
	if ! command -v curl >/dev/null 2>&1; then
		red "curl available (for --live)"
	else
		# Site root + a few key routes.
		for route in / /index.html /manual.html /login.html /privacy.html /tos.html; do
			code=$(curl -s -L -o /dev/null -w '%{http_code}' --max-time 15 "$SITE_BASE$route")
			if [ "$code" = "200" ]; then
				green "GET $SITE_BASE$route -> 200"
			else
				red "GET $SITE_BASE$route -> $code (want 200)"
			fi
		done

		# Broker health.
		code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$BROKER_BASE/health")
		if [ "$code" = "200" ]; then
			green "GET $BROKER_BASE/health -> 200"
		else
			red "GET $BROKER_BASE/health -> $code (want 200)"
		fi

		# Credentialed-CORS preflight: a session/dashboard endpoint must ECHO the
		# web origin in Access-Control-Allow-Origin (never "*") and allow creds.
		# /balance is credentialed (corsCreds). Send a browser-style preflight.
		hdrs=$(curl -s -D - -o /dev/null --max-time 15 \
			-X OPTIONS \
			-H "Origin: $WEB_ORIGIN" \
			-H "Access-Control-Request-Method: GET" \
			-H "Access-Control-Request-Headers: content-type" \
			"$BROKER_BASE/balance")
		acao=$(printf '%s' "$hdrs" | tr -d '\r' \
			| awk -F': ' 'tolower($1)=="access-control-allow-origin"{print $2; exit}')
		acac=$(printf '%s' "$hdrs" | tr -d '\r' \
			| awk -F': ' 'tolower($1)=="access-control-allow-credentials"{print $2; exit}')
		if [ "$acao" = "$WEB_ORIGIN" ]; then
			green "broker CORS preflight echoes origin ($acao, creds=$acac)"
		elif [ "$acao" = "*" ]; then
			red "broker CORS preflight returned '*' (must echo $WEB_ORIGIN for credentialed CORS)"
		else
			red "broker CORS preflight Access-Control-Allow-Origin='$acao' (want $WEB_ORIGIN)"
			info "headers received:"
			printf '%s\n' "$hdrs" | sed 's/^/        | /' | head -n 12
		fi
	fi
fi

# ============================================================================
# SUMMARY
# ============================================================================
section "SUMMARY"
printf '  passed: %s   failed: %s\n' "$PASSES" "$FAILS"
if [ "$FAILS" -eq 0 ]; then
	printf '\nSMOKE: PASS\n'
	exit 0
else
	printf '%s\n' "  failed checks:$FAILED_NAMES"
	printf '\nSMOKE: FAIL (%s)\n' "$FAILS"
	exit 1
fi
