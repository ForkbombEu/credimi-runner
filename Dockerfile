FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        unzip \
        bash \
        jq \
        openjdk-17-jre-headless \
        usbutils \
    && rm -rf /var/lib/apt/lists/*

# Install Android platform-tools (adb/fastboot)
RUN curl -fsSLo /tmp/platform-tools.zip https://dl.google.com/android/repository/platform-tools-latest-linux.zip \
    && mkdir -p /opt/android \
    && unzip -q /tmp/platform-tools.zip -d /opt/android \
    && rm -f /tmp/platform-tools.zip \
    && ln -s /opt/android/platform-tools/adb /usr/local/bin/adb \
    && ln -s /opt/android/platform-tools/fastboot /usr/local/bin/fastboot

# Install Maestro via official installer
RUN curl -fsSL https://get.maestro.mobile.dev | bash \
    && ln -s /root/.maestro/bin/maestro /usr/local/bin/maestro

# Add entrypoint script
COPY entrypoint.sh /usr/local/bin/phone-connect
RUN chmod +x /usr/local/bin/phone-connect

ENV PATH="/opt/android/platform-tools:${PATH}"

ENTRYPOINT ["/usr/local/bin/phone-connect"]
CMD ["--help"]
