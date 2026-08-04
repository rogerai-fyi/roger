.PHONY: build demo clean kill test check site site-serve smoke smoke-live beta cover cover-html cover-gate cover-gate-fast tdd
GOTOOLCHAIN := local
export GOTOOLCHAIN

# VERSION stamps the client semver into the binary (main.Version) at link time.
# Default: the source fallback in cmd/rogerai/main.go. Override for a release/beta:
#   make build VERSION=4.8.0
#   make beta  VERSION=4.8.0-beta.1
VERSION ?=
VERSION_LDFLAGS := $(if $(VERSION),-X main.Version=$(VERSION),)

build:
	go build -o bin/rogerai-broker    ./cmd/rogerai-broker
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/roger ./cmd/rogerai
	ln -sf roger bin/rogerai          # back-compat alias: the command is `roger`, `rogerai` still works
	go build -o bin/tokenizer-sidecar ./cmd/tokenizer-sidecar

# beta: a single stamped, trimmed binary for the host platform, named by its semver
# (e.g. bin/roger-4.8.0-beta.1). Requires VERSION, which must be a semver.
beta:
	@test -n "$(VERSION)" || { echo "usage: make beta VERSION=4.8.0-beta.1"; exit 1; }
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=$(VERSION)" -o bin/roger-$(VERSION) ./cmd/rogerai
	@echo "built bin/roger-$(VERSION)"

# Build the marketing + account site: resolve the shared chrome partials
# (web/src/_partials/{head,brand,nav,footer}.html) into every page and copy
# assets, writing the static tree to web/dist/. Same command DO App Platform
# runs (build_command). To change the logo, edit web/src/_partials/brand.html.
site:
	node web/build.mjs

# Build, then serve the output for a quick local check at http://localhost:5173
site-serve: site
	cd web/dist && python3 -m http.server 5173

# Run the full test suite (ledger/payouts/account etc. live in internal/store +
# cmd/rogerai-broker).
#
# -race is on by default here. Concurrency tests that only fail under the detector are
# decoration without it, and a review found we had several: the keyring and the Tower
# admission registry both assert properties that the plain runner cannot observe.
test:
	go test -race ./...

# ---- spec-first TDD / coverage (see TDD-WORKFLOW.md) -------------------------
# cover: full self-coverage profile across the module + the total line.
cover:
	go test -covermode=atomic -coverprofile=cover.out ./...
	@go tool cover -func=cover.out | tail -1

# cover-html: per-file green/red drill-down (also what we publish to GitHub Pages).
cover-html: cover
	go tool cover -html=cover.out -o coverage.html
	@echo "wrote coverage.html"

# cover-gate: THE GATE - no zero-coverage package + per-package floors + total floor.
# Run by CI and the repo-local pre-push hook. Bypass a local push with COVER_GATE_SKIP=1.
cover-gate:
	@scripts/cover-gate.sh

# cover-gate-fast: the FAST gate for DOC/WEB-ONLY pushes (no .go changed). Coverage cannot
# regress without Go changes, so this SKIPS the slow Postgres coverage and only sanity-checks:
# go build + vet + the web build + the manual version-sync. The repo-local pre-push hook
# auto-selects this when a push touches no .go files; otherwise the full `make cover-gate` runs.
# Do NOT use this for a Go change - it does not measure coverage. (Phase 5 E3.)
cover-gate-fast:
	@echo "[cover-gate-fast] no-Go push: build + vet + web build + version-sync (Postgres coverage skipped)"
	@go build ./...
	@go vet ./...
	@node web/build.mjs >/dev/null
	@ver=$$(sed -n 's/^\(const\|var\) Version = "\([0-9][^"]*\)".*/\2/p' cmd/rogerai/main.go | head -n1); \
		count=$$(grep -o 'data-cli-version>v[^<]*<' web/dist/manual.html | grep -c "^data-cli-version>v$$ver<$$" || true); \
		newest=$$(sed -n '/id="schangelog"/,$$p' web/dist/manual.html | grep -o 'man-plate__k">v[^<]*<' | head -n1); \
		if [ "$$count" -eq 2 ] && [ "$$newest" = "man-plate__k\">v$$ver<" ]; then echo "[cover-gate-fast] OK - manual release metadata matches v$$ver"; \
		else echo "[cover-gate-fast] FAIL - sync manual cover, reference, and newest changelog to v$$ver"; exit 1; fi

# hooks: install the repo-local pre-push gate from its tracked source.
# .git/hooks is not version controlled, so without this the gate exists on whichever
# machine happened to create it and nowhere else. Idempotent; run it after cloning.
.PHONY: hooks
hooks:
	@HOOKS="$$(git rev-parse --git-common-dir)/hooks"; \
	mkdir -p "$$HOOKS"; \
	install -m 0755 scripts/hooks/pre-push "$$HOOKS/pre-push"; \
	echo "[hooks] installed $$HOOKS/pre-push from scripts/hooks/pre-push"

# web-gate: what a push that touches web/ must clear.
#
# cover-gate-fast checks that Go still builds and the manual version is in sync, but
# it never ran the web suite and never crawled the built links. So the pushes MOST
# likely to break the website - the web-only ones - were getting the LEAST
# verification, while npm test and the link crawl only ran when some unrelated .go
# file happened to change. This is that missing half, and the pre-push hook runs it
# whenever web/ is in the range.
#
# No Postgres, no coverage profile: npm test builds dist and asserts over it, then the
# smoke package crawls that same dist for dead internal links and version drift.
.PHONY: web-gate
web-gate:
	@cd web && npm test
	@go test ./test/smoke/
	@echo "[web-gate] OK - web suite + built-link crawl"

# tdd: red-green watch loop for one package - make tdd PKG=./internal/store
tdd:
	@command -v gotestsum >/dev/null 2>&1 || go install gotest.tools/gotestsum@latest
	gotestsum --watch --format testname -- -count=1 $(PKG)

# The CI gate: build, vet, test, and a gofmt cleanliness check.
check:
	go build ./...
	go vet ./...
	go test ./...
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@echo "check: ok"

# The RELEASE GATE. Run this green before every `git tag`. It runs build + vet +
# gofmt + the regression suite, then builds web/dist, serves it, asserts every
# page returns 200, and crawls every internal <a href> to catch clean-URL 404s.
# Exits non-zero on any failure and prints a single SMOKE: PASS/FAIL line.
smoke:
	@scripts/smoke.sh

# Same gate, plus live production checks (rogerai.fm + broker.rogerai.fm/health
# + a credentialed-CORS preflight assertion). Needs network.
smoke-live:
	@scripts/smoke.sh --live
	@$(MAKE) --no-print-directory verify-artifacts

# Assert every RogerAI-OWNED artifact the site ADVERTISES - HuggingFace weights,
# GitHub recipe/eval trees - is reachable by a stranger with no credentials.
#
# v5.4.8 shipped a homepage headline reading "WAVE MICRO v1.0 - AVAILABLE", a
# "Download or Run Wave" button, and schema.org SoftwareSourceCode metadata, all
# pointing at a HuggingFace repo that answered 401 and two GitHub trees that
# answered 404 - with the 155-test web suite green the entire time. A release
# claim is a NETWORK fact; no offline test can see it. Needs network.
.PHONY: verify-artifacts
verify-artifacts:
	@cd web && npm run --silent verify:artifacts

# cross-compile the client for all platforms (single static binary each).
# CGO_ENABLED=0 => no libc dependency, so one Linux binary runs on glibc
# (Debian/Ubuntu/Fedora/Arch/Gentoo/openSUSE/Bazzite) AND musl (Alpine).
# Mirrors .github/workflows/release.yml.
.PHONY: release
release:
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "build $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o bin/roger-$$os-$$arch$$ext ./cmd/rogerai; \
	done
	@command -v sha256sum >/dev/null 2>&1 && (cd bin && sha256sum roger-* > checksums.txt && echo "wrote bin/checksums.txt") || true

demo: build
	@bash scripts/demo.sh

kill:
	-@for p in 7070 7072; do pid=$$(ss -tlnpH 2>/dev/null | grep "127.0.0.1:$$p" | grep -oP 'pid=\K[0-9]+' | head -1); [ -n "$$pid" ] && kill $$pid 2>/dev/null; done

clean: kill
	rm -rf bin
