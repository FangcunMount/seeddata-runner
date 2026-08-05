# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.25.9-alpine3.22 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=arm64

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/seeddata ./cmd/seeddata

FROM alpine:3.22

ARG VCS_REF=unknown
ARG BUILD_TIME=unknown

RUN apk add --no-cache ca-certificates curl tzdata \
    && addgroup -S -g 10001 seeddata \
    && adduser -S -D -H -u 10001 -G seeddata seeddata

LABEL org.opencontainers.image.source="https://github.com/FangcunMount/seeddata-runner" \
      org.opencontainers.image.revision="$VCS_REF" \
      org.opencontainers.image.created="$BUILD_TIME"

WORKDIR /app
COPY --from=builder /out/seeddata /usr/local/bin/seeddata

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/seeddata"]
CMD ["--config", "/run/seeddata/config.yaml"]
