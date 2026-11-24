# syntax=docker/dockerfile:1.7
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY . .
ARG VERSION=dev
ARG REVISION=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/felixapel/calibre-bookwarden/internal/buildinfo.Version=${VERSION} -X github.com/felixapel/calibre-bookwarden/internal/buildinfo.Revision=${REVISION}" \
      -o /out/bookwarden ./cmd/bookwarden

# Fast OCR models for English, German and Spanish, pinned by commit and hash.
FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS tessdata
ARG TESSDATA_COMMIT=87416418657359cb625c412a48b6e1d6d41c29bd
# set -e makes a failed download or checksum inside the loop fail the build.
RUN set -eu; \
    apk add --no-cache curl; \
    mkdir /tessdata; \
    cd /tessdata; \
    for spec in \
      "eng 7d4322bd2a7749724879683fc3912cb542f19906c83bcc1a52132556427170b2" \
      "deu 19d219bbb6672c869d20a9636c6816a81eb9a71796cb93ebe0cb1530e2cdb22d" \
      "spa 6f2e04d02774a18f01bed44b1111f2cd7f3ba7ac9dc4373cd3f898a40ea6b464"; do \
      set -- $spec; \
      curl -fsSL --proto '=https' -o "$1.traineddata" \
        "https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/${TESSDATA_COMMIT}/$1.traineddata"; \
      echo "$2  $1.traineddata" | sha256sum -c -; \
    done; \
    echo "$TESSDATA_COMMIT" > COMMIT # read at start by ocr.ToolVersions (ADR-039)

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# Alpine's tesseract ships configs/, tessconfigs/ and pdf.ttf but no models
# (checked 2026-10-12). Keep them: the models are added next to them.
RUN apk add --no-cache ca-certificates tesseract-ocr poppler-utils
COPY --from=tessdata /tessdata/ /usr/share/tessdata/
COPY --from=build /out/bookwarden /usr/local/bin/bookwarden
ENV TESSDATA_PREFIX=/usr/share/tessdata
# A soft limit for Go's heap, near 60 % of the 2 GiB container limit, so the
# collector works harder before the kernel kills the process. Override it
# with the container's environment if you change the memory limit.
ENV GOMEMLIMIT=1200MiB
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --start-interval=2s --retries=3 \
  CMD ["/usr/local/bin/bookwarden", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/bookwarden"]
CMD ["serve"]
# Last, so a new revision doesn't rebuild the layers above.
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="bookwarden" \
      org.opencontainers.image.source="https://github.com/felixapel/calibre-bookwarden" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.revision="${REVISION}"
