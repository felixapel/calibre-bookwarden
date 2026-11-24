#!/bin/sh
# Fails when an image's unpacked root filesystem is larger than its budget in
# MB (decimal). It measures inside the image, because `docker image inspect`
# reports the compressed size on the containerd image store (Docker 29's
# default) but the unpacked size on overlay2.
set -eu
image=$1
max_mb=$2
kib=$(docker run --rm --network none --user 0 --entrypoint du "$image" -sxk / | cut -f1)
mb=$(( (kib * 1024 + 999999) / 1000000 ))
echo "$image: ${mb} MB unpacked (budget ${max_mb} MB)"
[ "$mb" -le "$max_mb" ]
