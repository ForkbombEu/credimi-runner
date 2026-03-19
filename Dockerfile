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
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags=credimi_extra -o /out/credimi-runner main.go

FROM ghcr.io/forkbombeu/avdctl:latest AS avdctl


############################
# Base runtime (physical devices)
############################
FROM debian:bookworm-slim AS device

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
    ffmpeg \
    fontconfig \
    fonts-noto-mono \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /etc/fonts/conf.d && \
    printf '<!DOCTYPE fontconfig SYSTEM "fonts.dtd">\n<fontconfig>\n  <alias>\n    <family>monospace</family>\n    <prefer>\n      <family>Noto Color Emoji</family>\n    </prefer>\n  </alias>\n</fontconfig>' \
    > /etc/fonts/conf.d/51-emoji-monospace.conf && \
    fc-cache -fv

ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    LC_CTYPE=C.UTF-8 \
    JAVA_TOOL_OPTIONS="-Dfile.encoding=UTF-8"

# Install Maestro via official installer
RUN curl -fsSL https://get.maestro.mobile.dev | bash \
    && ln -s /root/.maestro/bin/maestro /usr/local/bin/maestro

COPY --from=builder /out/credimi-runner /usr/local/bin/credimi-runner
COPY --from=avdctl /usr/local/bin/avdctl /usr/local/bin/avdctl
COPY --from=builder /src/pkg/server/docs /src/pkg/server/docs
COPY --from=builder /src/pkg/gen/http /src/pkg/gen/http
RUN chmod +x /usr/local/bin/credimi-runner /usr/local/bin/avdctl

ENV CREDIMI_TEMP_DIR=/credimi/
RUN mkdir -p ${CREDIMI_TEMP_DIR}/workflows

# Physical-device entrypoint
COPY scripts/entrypoint.sh /usr/local/bin/phone-connect
RUN chmod +x /usr/local/bin/phone-connect

ENTRYPOINT ["/usr/local/bin/phone-connect"]
CMD ["--help"]

############################
# Phone runtime alias (for CI target compatibility)
############################
FROM device AS phone

############################
# Emulator runtime (extends device)
############################
FROM device AS emulator

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
    wget \
    git \
    psmisc \
    qemu-kvm \
    qemu-utils \
    libvirt-daemon-system \
    libvirt-clients \
    bridge-utils \
    && rm -rf /var/lib/apt/lists/*



WORKDIR /opt

# Android SDK
ENV ANDROID_SDK_ROOT=/opt/android-sdk
ENV ANDROID_HOME=$ANDROID_SDK_ROOT
ENV PATH=$ANDROID_SDK_ROOT/platform-tools:$ANDROID_SDK_ROOT/emulator:$ANDROID_SDK_ROOT/cmdline-tools/latest/bin:$ANDROID_SDK_ROOT/build-tools/35.0.0:$PATH

RUN mkdir -p $ANDROID_SDK_ROOT/cmdline-tools \
    && wget -q https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip -O cmdline-tools.zip \
    && unzip -q cmdline-tools.zip -d $ANDROID_SDK_ROOT/cmdline-tools \
    && mv $ANDROID_SDK_ROOT/cmdline-tools/cmdline-tools $ANDROID_SDK_ROOT/cmdline-tools/latest \
    && rm cmdline-tools.zip

RUN --mount=type=cache,target=/opt/android-sdk/.android/cache \
    --mount=type=cache,target=/opt/android-sdk/system-images \
    yes | sdkmanager --licenses > /dev/null || true

RUN sdkmanager --update
RUN sdkmanager --install \
    "platform-tools" \
    "platforms;android-35" \
    "system-images;android-35;google_apis_playstore;x86_64" \
    "emulator" \
    "build-tools;35.0.0"

# AVDs are stored outside /root/.android so mounting adb keys does not hide base AVDs.
ENV ANDROID_AVD_HOME=/avd-home
ENV AVDCTL_GOLDEN_DIR=/avd-golden
RUN mkdir -p ${ANDROID_AVD_HOME} ${AVDCTL_GOLDEN_DIR}

ARG ADB_PRIVATE_KEY
ARG ADB_PUBLIC_KEY
RUN set -eux; \
    mkdir -p /root/.android; \
    if [ -n "${ADB_PRIVATE_KEY:-}" ]; then \
    printf "%s\n" "$ADB_PRIVATE_KEY" > /root/.android/adbkey; \
    chmod 600 /root/.android/adbkey; \
    fi; \
    if [ -n "${ADB_PUBLIC_KEY:-}" ]; then \
    printf "%s\n" "$ADB_PUBLIC_KEY" > /root/.android/adbkey.pub; \
    chmod 644 /root/.android/adbkey.pub; \
    fi


ENTRYPOINT ["/usr/local/bin/phone-connect"]
CMD ["--emulator"]
