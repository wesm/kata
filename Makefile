.PHONY: build install test test-short lint vet clean fmt nilaway

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

clean:
	rm -f kata kata.exe coverage.out
	rm -rf dist
