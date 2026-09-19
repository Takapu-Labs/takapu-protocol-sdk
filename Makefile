GOLANGCI_LINT_VERSION := v2.13.2
GOLANGCI_LINT := $(CURDIR)/.bin/$(GOLANGCI_LINT_VERSION)/golangci-lint
GOVULNCHECK_VERSION := v1.8.0
GOVULNCHECK := $(CURDIR)/.bin/$(GOVULNCHECK_VERSION)/govulncheck

.PHONY: test vet build fmt lint lint-install vuln check example generate generate-check

test:
	go test -race ./...

vet:
	go vet ./...

build:
	go build ./...

fmt: $(GOLANGCI_LINT)
	"$(GOLANGCI_LINT)" fmt ./...

lint: $(GOLANGCI_LINT)
	"$(GOLANGCI_LINT)" config verify
	"$(GOLANGCI_LINT)" run ./...

lint-install: $(GOLANGCI_LINT)

vuln: $(GOVULNCHECK)
	"$(GOVULNCHECK)" ./...

$(GOVULNCHECK):
	GOBIN="$(@D)" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(GOLANGCI_LINT):
	@set -eu; \
	installer=$$(mktemp); \
	trap 'rm -f "$$installer"' EXIT; \
	curl -fsSL "https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh" -o "$$installer"; \
	sh "$$installer" -b "$(@D)" "$(GOLANGCI_LINT_VERSION)"

check: test vet build lint vuln

example:
	go run ./examples/protocol

generate:
	./proto/generate.sh

generate-check:
	./proto/generate.sh --check
