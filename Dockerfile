# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/d7y-cattle-operator ./cmd/operator

# Test image: contains the toolchain, sources, and module cache.
# Usage: docker run --rm d7y-cattle-operator:test go test ./...
FROM build AS test
RUN go vet ./...

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -g 65532 -S nonroot \
    && adduser -u 65532 -S -G nonroot -H nonroot
COPY --from=build /out/d7y-cattle-operator /d7y-cattle-operator
USER 65532:65532
ENTRYPOINT ["/d7y-cattle-operator"]
