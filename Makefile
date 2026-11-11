# Local Media build. `make verify` is what must pass before anything lands.
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: web build test e2e verify cross

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o localmedia ./cmd/localmedia

test:
	go vet ./...
	go test ./...

e2e: build
	node e2e/run.mjs

# Every platform the plan promises, built from one machine with cgo off.
cross: web
	@for t in windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o release/localmedia-$$os-$$arch$$ext ./cmd/localmedia || exit 1; \
	done

verify: test e2e
