FROM golang:1.25.1-alpine AS builder

RUN apk add --no-cache git gcc g++ musl-dev linux-headers libstdc++-dev

WORKDIR /app

COPY . .
RUN go mod download

# Build beacon-chain and validator binaries
RUN go build -o /out/beacon-chain ./cmd/beacon-chain
RUN go build -o /out/validator ./cmd/validator

# Run tests for the packages affected by the proposer preferences fix
RUN go test ./beacon-chain/verification/ ./beacon-chain/sync/ -count=1 -v

# Minimal runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tini

COPY --from=builder /out/beacon-chain /beacon-chain
COPY --from=builder /out/validator /validator

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/beacon-chain"]
