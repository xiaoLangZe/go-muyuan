# go-muyuan 开发任务入口：test/vet/lint/cover/bench/docs
# 用法：make <目标>（GNU make；Windows 可用 Git Bash 或 WSL）

GO ?= go

.PHONY: all
all: fmt-check vet test

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then echo "需要 gofmt:"; echo "$$files"; exit 1; fi

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test
test:
	$(GO) test -count=1 ./...

.PHONY: race
race:
	$(GO) test -race -count=1 ./...

.PHONY: cover
cover:
	$(GO) test -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: bench
bench:
	$(GO) test -bench=. -benchtime=3s -count=3 -run='^$$' .

.PHONY: docs
docs:
	cd wiki && npm ci && npm run build

.PHONY: docs-dev
docs-dev:
	cd wiki && npm run dev