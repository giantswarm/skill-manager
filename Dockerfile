# The Go binary is built by CircleCI (architect/go-build) and attached to the
# build context as <binary>-<os>-<arch>; this image only assembles the runtime:
# the binary on alpine with the CA certificates it reaches GitHub, OCI
# registries and Dex with.
#
# For a local build, produce the binary first:
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o skill-manager-linux-amd64 .
FROM gsoci.azurecr.io/giantswarm/alpine:3.24.4

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates \
    && addgroup -g 1000 -S giantswarm \
    && adduser -u 1000 -S giantswarm -G giantswarm
USER giantswarm

COPY skill-manager-${TARGETOS}-${TARGETARCH} /skill-manager

ENTRYPOINT ["/skill-manager"]
CMD ["serve"]
