.PHONY: build demo clean kill
GOTOOLCHAIN := local
export GOTOOLCHAIN

build:
	go build -o bin/rogerai-broker ./cmd/rogerai-broker
	go build -o bin/rogerai        ./cmd/rogerai

# cross-compile the client for all platforms (single static binary each).
# CGO_ENABLED=0 => no libc dependency, so one Linux binary runs on glibc
# (Debian/Ubuntu/Fedora/Arch/Gentoo/openSUSE/Bazzite) AND musl (Alpine).
# Mirrors .github/workflows/release.yml.
.PHONY: release
release:
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "build $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o bin/rogerai-$$os-$$arch$$ext ./cmd/rogerai; \
	done
	@command -v sha256sum >/dev/null 2>&1 && (cd bin && sha256sum rogerai-* > checksums.txt && echo "wrote bin/checksums.txt") || true

demo: build
	@bash scripts/demo.sh

kill:
	-@for p in 7070 7072; do pid=$$(ss -tlnpH 2>/dev/null | grep "127.0.0.1:$$p" | grep -oP 'pid=\K[0-9]+' | head -1); [ -n "$$pid" ] && kill $$pid 2>/dev/null; done

clean: kill
	rm -rf bin
