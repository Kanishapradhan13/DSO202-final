#!/usr/bin/env bash
# Regenerates Go, gRPC and grpc-gateway code from proto/ into gen/go/.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$(go env GOPATH)/bin:$PATH"
rm -rf gen/go && mkdir -p gen/go
protoc -I proto -I proto/third_party \
  --go_out=gen/go --go_opt=paths=source_relative \
  --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=gen/go --grpc-gateway_opt=paths=source_relative \
  proto/bank/*/v1/*.proto
echo "generated $(find gen/go -name '*.go' | wc -l | tr -d ' ') files"
