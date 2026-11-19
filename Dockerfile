# syntax=docker/dockerfile:1

# Conversion binaries come from the official nydus v2.4.5 static release.
# The chart runs /registry-proxy with conversion.nydusify=/usr/local/bin/nydusify
# and conversion.nydusImage=/usr/local/bin/nydus-image.
FROM alpine:3.20 AS nydus
ARG NYDUS_VERSION=v2.4.5
ARG NYDUS_SHA256=1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072
RUN apk add --no-cache ca-certificates curl \
    && curl -fsSL --retry 3 -o /tmp/nydus.tgz \
       "https://github.com/dragonflyoss/nydus/releases/download/${NYDUS_VERSION}/nydus-static-${NYDUS_VERSION}-linux-amd64.tgz" \
    && echo "${NYDUS_SHA256}  /tmp/nydus.tgz" | sha256sum -c - \
    && tar -xzf /tmp/nydus.tgz -C /tmp \
    && install -m 0755 /tmp/nydus-static/nydusify /tmp/nydus-static/nydus-image /usr/local/bin/ \
    && rm -rf /tmp/nydus.tgz /tmp/nydus-static

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/d7y-cattle-operator ./cmd/operator \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/registry-proxy ./cmd/registry-proxy \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admission-webhook ./cmd/admission-webhook

# Test image: contains the toolchain, sources, and module cache.
# Usage: docker run --rm d7y-cattle-operator:test go test ./...
FROM build AS test
RUN go vet ./...

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -g 65532 -S nonroot \
    && adduser -u 65532 -S -G nonroot -H nonroot
COPY --from=build /out/d7y-cattle-operator /d7y-cattle-operator
COPY --from=build /out/registry-proxy /registry-proxy
COPY --from=build /out/admission-webhook /admission-webhook
COPY --from=nydus /usr/local/bin/nydusify /usr/local/bin/nydusify
COPY --from=nydus /usr/local/bin/nydus-image /usr/local/bin/nydus-image
USER 65532:65532
ENTRYPOINT ["/d7y-cattle-operator"]
