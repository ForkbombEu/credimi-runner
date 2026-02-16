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

# Maestro patch 
RUN curl -fsSL https://raw.githubusercontent.com/omnarayan/Maestro/feature/driver-host-port-distribution/upgrade-maestro-ports.sh | bash


COPY --from=builder /out/credimi-runner /usr/local/bin/credimi-runner
COPY --from=builder /src/pkg/server/docs /src/pkg/server/docs
COPY --from=builder /src/pkg/gen/http /src/pkg/gen/http
RUN chmod +x /usr/local/bin/credimi-runner

# Physical-device entrypoint
COPY scripts/entrypoint.sh /usr/local/bin/phone-connect
RUN chmod +x /usr/local/bin/phone-connect

ENTRYPOINT ["/usr/local/bin/phone-connect"]
CMD ["--help"]


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
        bridge-utils 

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

# AVD dirs + optional adb keys baked at build time
ENV ANDROID_AVD_HOME=/root/.android/avd
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

# Golden images ONLY in emulator target
ARG BASE_URL=https://files.pn-a.com/api/static
ADD ${BASE_URL}/credimi_base_image.tar.gz /tmp/credimi_base_image.tar.gz
ADD ${BASE_URL}/credimi_golden.tar.gz     /tmp/credimi_golden.tar.gz

RUN set -eux; \
  test -s /tmp/credimi_base_image.tar.gz; \
  test -s /tmp/credimi_golden.tar.gz; \
  tar -xzf /tmp/credimi_base_image.tar.gz -C "${ANDROID_AVD_HOME}"; \
  tar -xzf /tmp/credimi_golden.tar.gz     -C "${AVDCTL_GOLDEN_DIR}"; \
  rm -f /tmp/credimi_base_image.tar.gz /tmp/credimi_golden.tar.gz


ENTRYPOINT ["/usr/local/bin/phone-connect"]
CMD ["--emulator"]