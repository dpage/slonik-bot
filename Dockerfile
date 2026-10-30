FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/dpage/slonik-bot/internal/version.Version=${VERSION}" -o /slonik-bot ./cmd/slonik-bot

# Debian's chromium is used because Google ships no Linux arm64 Chrome, and
# the image has to run on Apple Silicon as well as x86 hosts.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium xvfb xauth fonts-dejavu-core fonts-noto-color-emoji ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 1000 slonik \
 && mkdir -p /config /notes \
 && chown slonik:slonik /config /notes \
 && mkdir -m 1777 /tmp/.X11-unix

# Chromium's sandbox needs unprivileged user namespaces, which Docker's default
# seccomp profile blocks, so Chromium would refuse to start. The container is
# the isolation boundary here, so the sandbox is turned off for this image only.
# The Debian /usr/bin/chromium wrapper resets CHROMIUM_FLAGS itself, so an ENV
# would be ignored; it does source every file in /etc/chromium.d, which works.
RUN echo 'export CHROMIUM_FLAGS="$CHROMIUM_FLAGS --no-sandbox"' > /etc/chromium.d/no-sandbox
COPY --from=build /slonik-bot /usr/local/bin/slonik-bot
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
ENV XDG_CONFIG_HOME=/config
USER slonik
VOLUME ["/config", "/notes"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
