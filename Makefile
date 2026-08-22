.PHONY: build demo clean kill test test-db check site site-serve smoke smoke-live beta cover cover-html cover-gate cover-gate-fast tdd reach
GOTOOLCHAIN := local
export GOTOOLCHAIN

# VERSION stamps the client semver into the binary (main.Version) at link time.
# Default: the source fallback in cmd/rogerai/main.go. Override for a release/beta:
#   make build VERSION=4.8.0
#   make beta  VERSION=4.8.0-beta.1
VERSION ?=
VERSION_LDFLAGS := $(if $(VERSION),-X main.Version=$(VERSION),)
# roger-tower carries its own symbol: `var version` in package main, LOWERCASE, where the
# client has `var Version`. The distinction is load-bearing rather than cosmetic - the Go
# linker SILENTLY IGNORES -X for a symbol that does not exist, so stamping the tower with
# VERSION_LDFLAGS above would look correct in the recipe, exit 0, and ship a binary still
# reporting "dev". Keep this in step with the tower ldflags in .goreleaser.yaml, which is
# what stamps the binaries an operator actually downloads.
TOWER_VERSION_LDFLAGS := $(if $(VERSION),-X main.version=$(VERSION),)

build:
	go build -o bin/rogerai-broker    ./cmd/rogerai-broker
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/roger ./cmd/rogerai
	ln -sf roger bin/rogerai          # back-compat alias: the command is `roger`, `rogerai` still works
	go build -o bin/tokenizer-sidecar ./cmd/tokenizer-sidecar
	go build -ldflags "$(TOWER_VERSION_LDFLAGS)" -o bin/roger-tower ./cmd/roger-tower

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

# ---- the DATABASE-backed suites, which `make test` silently skips ------------
#
# Every PG-backed suite in this tree does `t.Skip` unless ROGERAI_TEST_DATABASE_URL is set,
# so a green `make test` says NOTHING about the durable stores: not the migrations, not the
# column lists, not mem/PG parity. CI sets it (see .github/workflows/coverage.yml) and a
# developer almost never does, which is how a dropped column in PGStore.ByTower once sat
# behind a passing parity suite.
#
# TWO things have to be right, and both are easy to get wrong by hand:
#
#   the schema  - production provisions `rogerai` out-of-band and NewPostgres owns only the
#                 tables inside it, by design. A bare container has no such schema, and the
#                 suites that do not create their own (admit, enroll) fail with
#                 `schema "rogerai" does not exist` in a way that reads like a code defect.
#
#   -p 1        - a dozen suites TRUNCATE tables in that ONE shared schema, so packages run
#                 in parallel wipe each other's fixtures. It scales with core count: a
#                 2-core CI runner almost never trips it and a 64-core workstation trips it
#                 most runs, which is the worst possible distribution for believing a
#                 failure. Serial here costs minutes and buys a result you can act on.
#
# Usage: make test-db            (starts and stops its own postgres:16)
#        make test-db PKGS=./internal/towercore/attach/...
PG_TEST_PORT ?= 55432
PKGS ?= ./internal/towercore/... ./internal/store/...
.PHONY: test-db
test-db:
	@docker rm -f rogerai-test-pg >/dev/null 2>&1 || podman rm -f rogerai-test-pg >/dev/null 2>&1 || true
	@# WAIT FOR THE NAME TO BE FREE. `stop` and `rm` both return before the container is
	@# actually gone, so back-to-back runs used to start while the previous postgres was
	@# still shutting down: pg_isready answered from the dying instance, and the schema
	@# create then failed with "the database system is shutting down". A target whose whole
	@# purpose is a result you can act on must not have its own flake.
	@for i in $$(seq 1 60); do \
		if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx rogerai-test-pg \
			|| podman ps -a --format '{{.Names}}' 2>/dev/null | grep -qx rogerai-test-pg; then sleep 1; else break; fi; \
	done
	@(docker run -d --rm --name rogerai-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=roger_test \
		-p $(PG_TEST_PORT):5432 postgres:16 >/dev/null 2>&1 \
		|| podman run -d --rm --name rogerai-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=roger_test \
		-p $(PG_TEST_PORT):5432 postgres:16 >/dev/null) \
		&& echo "postgres:16 up on $(PG_TEST_PORT)"
	@until (docker exec rogerai-test-pg pg_isready -U postgres >/dev/null 2>&1 \
		|| podman exec rogerai-test-pg pg_isready -U postgres >/dev/null 2>&1); do sleep 1; done
	@# Retried: pg_isready can answer before the server will accept a real statement, and a
	@# single attempt here is the difference between a usable target and a coin flip.
	@for i in $$(seq 1 30); do \
		if (docker exec rogerai-test-pg psql -U postgres -d roger_test -c "CREATE SCHEMA IF NOT EXISTS rogerai;" >/dev/null 2>&1 \
			|| podman exec rogerai-test-pg psql -U postgres -d roger_test -c "CREATE SCHEMA IF NOT EXISTS rogerai;" >/dev/null 2>&1); then break; fi; \
		if [ $$i = 30 ]; then echo "could not create the rogerai schema" >&2; exit 1; fi; \
		sleep 1; \
	done
	@echo "running $(PKGS) with -p 1"
	@ROGERAI_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:$(PG_TEST_PORT)/roger_test?sslmode=disable" \
		go test -p 1 -count=1 $(PKGS); \
		status=$$?; \
		(docker stop rogerai-test-pg >/dev/null 2>&1 || podman stop rogerai-test-pg >/dev/null 2>&1 || true); \
		exit $$status

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

# The CI gate: build, vet, test, gofmt, and a reachability check.
check:
	go build ./...
	go vet ./...
	go test ./...
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@$(MAKE) --no-print-directory reach
	@echo "check: ok"

# reach: what is in the binary that nothing in the binary calls.
#
# Coverage says a function is EXERCISED, never that anything USES it, and the two come
# apart in the worst direction: a helper tested to 100% and wired to nothing looks
# healthier than the code that runs. `storeFor` - the Tower's whole durable-storage
# wiring - shipped that way, so a durable-profile Tower silently kept state on local disk.
.PHONY: reach
reach:
	@scripts/reachability.sh

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
