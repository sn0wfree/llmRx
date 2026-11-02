# Minimal runtime image built on `scratch`.
#
# The Go binary is statically linked (CGO + `-extldflags "-static"`)
# so it has zero runtime dependencies. The only extra pieces are:
#   - ca-certificates.crt   — outbound TLS to upstream LLM providers
#   - /etc/passwd + /etc/group — for `os/user.Lookup("llmrx")` during
#                                bootstrap (privilege drop)
#
# Final image size: ~7.9 MB (measured on the v0.1.0 build).
#
# Build:  ./scripts/build-docker.sh   (or `make docker-build`)
# Run:    docker compose up -d --build
#
# The gateway binary itself handles everything the docker side
# used to do via busybox + entrypoint script:
#   - master-key resolution (env → /data/llmrx.key → generate)
#   - bind-mount /data chown (root → llmrx, only if needed)
#   - privilege drop (setgid + setuid to llmrx)
#   - HEALTHCHECK probe (`llmRx -healthcheck 127.0.0.1:8787`)

FROM scratch

# --- TLS trust anchors (~250 KB) ---
COPY --from=alpine:3.20 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# --- minimal /etc/passwd + /etc/group for os/user.Lookup ---
COPY <<EOF /etc/passwd
root:x:0:0:root:/root:/bin/sh
llmrx:x:1000:1000:llmrx:/data:/bin/sh
EOF
COPY <<EOF /etc/group
root:x:0:
llmrx:x:1000:
EOF

# --- the gateway (statically linked, ~12 MB) ---
# BIN_PATH lets a multi-arch release build point at the right prebuilt
# binary without duplicating this Dockerfile. Local builds and docker.yml
# keep the default.
ARG BIN_PATH=build/llmRx
COPY ${BIN_PATH} /usr/local/bin/llmRx

# Start as root so the binary can chown bind-mounted /data and
# write the starter config.yml on a fresh volume. The binary
# drops privileges to llmrx internally via dropPrivileges() before
# opening the DB or listening on the port.
WORKDIR /data

EXPOSE 8787
VOLUME ["/data"]

ENV TZ=UTC

# Liveness via the gateway's GET /health. The binary itself
# implements the probe (raw TCP + HTTP/1.0 read), so we don't
# need wget/busybox in the image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/llmRx", "-healthcheck", "127.0.0.1:8787"]

ENTRYPOINT ["/usr/local/bin/llmRx"]
CMD ["-config", "/data/config.yml"]