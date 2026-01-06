# Minimal image with crane and tar for extracting container image layers
# Based on alpine for minimal footprint while having required tools
ARG CRANE_VERSION=v0.20.2

FROM alpine:3.20

ARG CRANE_VERSION
ARG TARGETARCH

# Install only what we need: ca-certificates for HTTPS, tar is included in alpine
RUN apk add --no-cache ca-certificates

# Download and install crane binary
RUN wget -O /tmp/crane.tar.gz \
    "https://github.com/google/go-containerregistry/releases/download/${CRANE_VERSION}/go-containerregistry_Linux_${TARGETARCH}.tar.gz" && \
    tar -xzf /tmp/crane.tar.gz -C /usr/local/bin crane && \
    chmod +x /usr/local/bin/crane && \
    rm /tmp/crane.tar.gz

# Run as non-root user
RUN adduser -D -u 65532 nonroot
USER nonroot

ENTRYPOINT ["/usr/local/bin/crane"]
