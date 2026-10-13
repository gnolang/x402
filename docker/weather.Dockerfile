# Image for the weather demo seller, examples/weather. Unlike gnofacilitator's,
# it builds the binary itself, so from the repository root
#
#   docker build -f docker/weather.Dockerfile .
#
# is the whole recipe, and the release workflow runs the same. The seller never
# touches the chain and holds no key: it needs a facilitator and an address to
# be paid at.
#
#   docker run --rm -p 8401:8401 ghcr.io/gnoverse/x402-weather:latest \
#     -network gno:onyx-1 -facilitator https://facilitator.onyx.testnets.gno.land \
#     -pay-to g1... -public-url https://<host>
#
# -listen already defaults to all interfaces on :8401, so publishing the port is
# enough.

# The compiler runs on the build host and cross-compiles, so a multi-platform
# build never emulates the Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.26.5@sha256:705e964a93a2fd2e75c7d59bb7d781b57e30f12293ffde5175c69229e18fb678 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Only the packages the seller is built from, so an edit elsewhere in the
# repository leaves the build cache warm.
COPY facilitator/ facilitator/
COPY server/ server/
COPY examples/weather/ examples/weather/
# Declared after the download, so both platforms share its layer.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/weather ./examples/weather

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/weather /usr/local/bin/weather
EXPOSE 8401
ENTRYPOINT ["/usr/local/bin/weather"]
