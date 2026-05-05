.PHONY: build install test test-short lint vet clean fmt nilaway tui tui-demo

GOFLAGS_TEST := -shuffle=on

build:
	go build -o kata ./cmd/kata

install:
	GOBIN=$${HOME}/.local/bin go install ./cmd/kata

test:
	go test $(GOFLAGS_TEST) ./...

test-short:
	go test -short $(GOFLAGS_TEST) ./...

lint:
	golangci-lint run --config .golangci.yml

vet:
	go vet ./...

nilaway:
	@if ! command -v nilaway >/dev/null 2>&1; then \
		echo "nilaway not found. Install with:" >&2; \
		echo "  go install go.uber.org/nilaway/cmd/nilaway@latest" >&2; \
		exit 1; \
	fi
	@module_path="$$(go list -m)" || { \
		echo "failed to determine module path" >&2; \
		exit 1; \
	}; \
		nilaway -include-pkgs="$$module_path" -test=false ./...

fmt:
	gofmt -w .

tui:
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	GOFLAGS=-buildvcs=false go build -o "$$tmp/kata" ./cmd/kata; \
	KATA_COLOR_MODE="$${KATA_COLOR_MODE:-dark}" "$$tmp/kata" tui

tui-demo:
	@tmp=$$(mktemp -d); \
	trap 'KATA_HOME="$$tmp/home" "$$tmp/kata" daemon stop >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT; \
	mkdir -p "$$tmp/ws"; \
	GOFLAGS=-buildvcs=false go build -o "$$tmp/kata" ./cmd/kata; \
	KATA_HOME="$$tmp/home" "$$tmp/kata" --workspace "$$tmp/ws" init --project github.com/wesm/kata --name kata >/dev/null; \
	KATA_HOME="$$tmp/home" "$$tmp/kata" --workspace "$$tmp/ws" --as alice create "fix login bug on Safari" --owner claude-4.7 --label tui --label ux >/dev/null; \
	KATA_HOME="$$tmp/home" "$$tmp/kata" --workspace "$$tmp/ws" --as wesm create "rebuild search index" --owner wesm --label infra >/dev/null; \
	KATA_HOME="$$tmp/home" "$$tmp/kata" --workspace "$$tmp/ws" --as bob close 2 >/dev/null; \
	KATA_HOME="$$tmp/home" "$$tmp/kata" --workspace "$$tmp/ws" --as alice create "purge stale tokens" --label cleanup >/dev/null; \
	KATA_HOME="$$tmp/home" KATA_COLOR_MODE=dark "$$tmp/kata" --workspace "$$tmp/ws" tui

clean:
	rm -f kata kata.exe coverage.out
	rm -rf dist
