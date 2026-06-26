.PHONY: build demo clean kill test check site site-serve smoke smoke-live
GOTOOLCHAIN := local
export GOTOOLCHAIN

build:
	go build -o bin/rogerai-broker    ./cmd/rogerai-broker
	go build -o bin/roger             ./cmd/rogerai
	ln -sf roger bin/rogerai          # back-compat alias: the command is `roger`, `rogerai` still works
	go build -o bin/tokenizer-sidecar ./cmd/tokenizer-sidecar

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
test:
	go test ./...

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

# Same gate, plus live production checks (rogerai.fyi + broker.rogerai.fyi/health
# + a credentialed-CORS preflight assertion). Needs network.
smoke-live:
	@scripts/smoke.sh --live

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
