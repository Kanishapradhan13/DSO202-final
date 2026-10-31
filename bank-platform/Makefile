SERVICES := api-gateway auth-service customer-service kyc-service account-service ledger-service \
            transaction-service transfer-service payment-service card-service loan-service \
            fraud-service audit-service notification-service statement-service
# Use go from PATH, falling back to the toolchain installed in ~/sdk/go.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/sdk/go/bin/go)
ARCH ?= $(shell docker info --format '{{.Architecture}}' 2>/dev/null | sed 's/x86_64/amd64/;s/aarch64/arm64/')

.PHONY: proto build test vet build-linux up up-fast down logs smoke clean

proto:            ## Regenerate gRPC / grpc-gateway code
	./scripts/gen-proto.sh

build:            ## Compile every service for the host
	$(GO) build ./...

test:             ## Unit tests with coverage
	$(GO) test -cover ./...

vet:
	$(GO) vet ./...

build-linux:      ## Static Linux binaries for Dockerfile.prebuilt
	@mkdir -p bin/linux
	@for s in $(SERVICES); do \
		echo "  building $$s ($(ARCH))"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) $(GO) build -trimpath -ldflags="-s -w" -o bin/linux/$$s ./services/$$s || exit 1; \
	done

up:               ## Full stack, images built inside Docker
	docker compose up -d --build

up-fast: build-linux  ## Full stack from host-built binaries (faster rebuilds)
	DOCKERFILE=deploy/docker/Dockerfile.prebuilt docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f --tail=50

smoke:            ## End-to-end journey through the gateway
	./scripts/smoke-test.sh

clean:
	rm -rf bin web/.next
