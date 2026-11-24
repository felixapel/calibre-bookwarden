# syntax=docker/dockerfile:1.7
# Disposable Calibre content server for bookwarden's protocol probe and
# integration tests. Never used in production.
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
ARG CALIBRE_VERSION=9.16.0
ARG CALIBRE_SHA512=1016283a6b8567eb2bf3405ad957e4abac4ca57d66ddc7927c46514e0e1e4cbe1bc33780846626997b943e3fa8fd7c92c2902d635e5ac9c7ad7d16999b7b4308
# Calibre keeps its config in the running user's ~/.config/calibre. Don't point
# it at a shared path: the root build steps below would create it, and the
# calibre user couldn't write to it at runtime.
ENV QT_QPA_PLATFORM=offscreen \
    PATH=/opt/calibre:$PATH
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl xz-utils libegl1 libglx0 libopengl0 libxkbcommon0 \
      libfontconfig1 libfreetype6 libnss3 libxcb-cursor0 ghostscript \
      poppler-utils tesseract-ocr tesseract-ocr-eng \
 && rm -rf /var/lib/apt/lists/* \
 && curl --fail --location --proto '=https' --tlsv1.2 \
      "https://download.calibre-ebook.com/${CALIBRE_VERSION}/calibre-${CALIBRE_VERSION}-x86_64.txz" \
      --output /tmp/calibre.txz \
 && echo "${CALIBRE_SHA512}  /tmp/calibre.txz" | sha512sum --check --strict \
 && mkdir -p /opt/calibre \
 && tar --extract --xz --file /tmp/calibre.txz --directory /opt/calibre \
 && rm /tmp/calibre.txz \
 && calibredb --version | grep -F "calibre ${CALIBRE_VERSION%.*}" \
 && useradd --uid 10002 --create-home --shell /usr/sbin/nologin calibre \
 && mkdir -p /library /auth \
 && chown calibre:calibre /library /auth
COPY --chmod=0755 build-fixture.sh entrypoint.sh /usr/local/bin/
USER calibre
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
