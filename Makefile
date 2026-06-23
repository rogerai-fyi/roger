.PHONY: build demo clean kill
GOTOOLCHAIN := local
export GOTOOLCHAIN

build:
	go build -o bin/rogerai-broker ./cmd/rogerai-broker
	go build -o bin/rogerai        ./cmd/rogerai

# cross-compile the client for all platforms (single static binary each)
.PHONY: release
release:
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "build $$os/$$arch"; \
	  GOOS=$$os GOARCH=$$arch go build -o bin/rogerai-$$os-$$arch$$ext ./cmd/rogerai; \
	done

demo: build
	@bash scripts/demo.sh

kill:
	-@for p in 7070 7072; do pid=$$(ss -tlnpH 2>/dev/null | grep "127.0.0.1:$$p" | grep -oP 'pid=\K[0-9]+' | head -1); [ -n "$$pid" ] && kill $$pid 2>/dev/null; done

clean: kill
	rm -rf bin
