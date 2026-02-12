# syntax=docker/dockerfile:1.4
FROM golang:1.25.5-bookworm AS builder

WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH
ENV GOCACHE=/go-cache
ENV GOMODCACHE=/gomod-cache

RUN apt-get update \
    && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=secret,id=credimi_extra_pat,required=true \
    --mount=type=cache,target=/gomod-cache \
    --mount=type=cache,target=/go-cache \
    pat="$(cat /run/secrets/credimi_extra_pat)" \
    && git config --global url."https://${pat}@github.com/".insteadOf "https://github.com/" \
    && go mod download \
    && rm -f /root/.gitconfig

COPY . ./
RUN --mount=type=cache,target=/gomod-cache --mount=type=cache,target=/go-cache go generate ./...
RUN --mount=type=cache,target=/gomod-cache --mount=type=cache,target=/go-cache \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/credimi-runner main.go

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        bash \
        jq \
        openjdk-17-jre-headless \
        usbutils \
        unzip \
        adb \
        aapt \
    && rm -rf /var/lib/apt/lists/*

# Install Maestro via official installer
RUN curl -fsSL https://get.maestro.mobile.dev | bash \
    && ln -s /root/.maestro/bin/maestro /usr/local/bin/maestro

COPY --from=builder /out/credimi-runner /usr/local/bin/credimi-runner
COPY --from=builder /src/pkg/server/docs /src/pkg/server/docs
COPY --from=builder /src/pkg/gen/http /src/pkg/gen/http
RUN chmod +x /usr/local/bin/credimi-runner

# Add entrypoint script
COPY entrypoint.sh /usr/local/bin/phone-connect
RUN chmod +x /usr/local/bin/phone-connect

ENTRYPOINT ["/usr/local/bin/phone-connect"]
CMD ["--help"]
