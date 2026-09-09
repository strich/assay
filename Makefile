# assay — build, test and release targets.

.PHONY: build vet test check fmt lint run e2e release

# Build the static binary.
build:
	go build -o bin/assay ./cmd/assay

# Static analysis across the module.
vet:
	go vet ./...

# Unit tests across the module.
test:
	go test ./...

# The gate CI runs: build + vet + tests.
check: build vet test

# Format every Go source file in place.
fmt:
	gofmt -w .

# Optional lint pass; no-op with a hint when golangci-lint is not installed.
lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || \
		echo "golangci-lint not installed; skipping (install: https://golangci-lint.run)"

# Show the CLI surface.
run:
	go run ./cmd/assay --help

# Offline end-to-end: real CLI, mock opencode, zero network.
e2e:
	bash test/e2e/run.sh

# Cross-compile static release binaries (CGO is not used anywhere).
release:
	mkdir -p dist
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/assay-linux-amd64       ./cmd/assay
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/assay-linux-arm64       ./cmd/assay
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/assay-darwin-arm64      ./cmd/assay
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/assay-windows-amd64.exe ./cmd/assay
