# syntax=docker/dockerfile:1.4

FROM golang:1.26.5-bookworm AS builder

WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG BUILD_TIME
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
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags=credimi_extra \
    -ldflags "-s -w -X github.com/forkbombeu/credimi-runner/internal/buildinfo.Version=${VERSION} -X github.com/forkbombeu/credimi-runner/internal/buildinfo.BuildTime=${BUILD_TIME}" \
    -o /out/credimi-runner main.go

FROM ubuntu:24.04
COPY --from=builder /out/credimi-runner /usr/local/bin/credimi-runner
ENV DEBIAN_FRONTEND=noninteractive \
    ANDROID_SDK_ROOT=/opt/android-sdk \
    ANDROID_HOME=/opt/android-sdk \
    ANDROID_AVD_HOME=/root/.android/avd \
    ANDROID_SDK_BOOTSTRAP=/opt/android-sdk-bootstrap \
    PATH=/opt/android-sdk/platform-tools:/opt/android-sdk/emulator:/opt/android-sdk/cmdline-tools/latest/bin:/opt/android-sdk-bootstrap/cmdline-tools/latest/bin:/root/.maestro/bin:$PATH
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl bash openjdk-17-jre-headless openssh-client ffmpeg \
    unzip wget qemu-kvm qemu-utils libxkbfile1 libxcomposite1 libxcursor1 libxi6 \
    libxrandr2 libxtst6 libnss3 libxdamage1 libxrender1 libatk1.0-0 libcairo2 \
    libdbus-1-3 libgl1 libgtk-3-0 libpulse0 && \
    mkdir -p "$ANDROID_SDK_BOOTSTRAP/cmdline-tools" /opt/android-sdk && \
    curl -fsSL https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip -o /tmp/android-cmdline-tools.zip && \
    unzip -q /tmp/android-cmdline-tools.zip -d "$ANDROID_SDK_BOOTSTRAP/cmdline-tools" && \
    mv "$ANDROID_SDK_BOOTSTRAP/cmdline-tools/cmdline-tools" "$ANDROID_SDK_BOOTSTRAP/cmdline-tools/latest" && \
    yes | sdkmanager --sdk_root="$ANDROID_SDK_BOOTSTRAP" --licenses >/dev/null && \
    sdkmanager --sdk_root="$ANDROID_SDK_BOOTSTRAP" "platform-tools" "build-tools;35.0.0" && \
    ln -s "$ANDROID_SDK_BOOTSTRAP/platform-tools/adb" /usr/local/bin/adb && \
    ln -s "$ANDROID_SDK_BOOTSTRAP/build-tools/35.0.0/aapt2" /usr/local/bin/aapt2 && \
    curl -fsSL https://get.maestro.mobile.dev | bash && \
    chmod 0555 /usr/local/bin/credimi-runner && \
    rm -rf /var/lib/apt/lists/* /tmp/android-cmdline-tools.zip
ENTRYPOINT ["/usr/local/bin/credimi-runner"]
