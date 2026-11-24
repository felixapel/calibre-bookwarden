#!/bin/sh
# Builds the fixture library and two accounts on first start, then serves it.
# Passwords come from the environment because this container is disposable.
set -eu
: "${BW_READER_PASSWORD:?set BW_READER_PASSWORD}"
: "${BW_WRITER_PASSWORD:?set BW_WRITER_PASSWORD}"
if [ ! -f /library/metadata.db ]; then
  build-fixture.sh /library
fi
if [ ! -f /auth/users.sqlite ]; then
  calibre-server --userdb /auth/users.sqlite --manage-users -- \
    add reader "$BW_READER_PASSWORD" --readonly
  calibre-server --userdb /auth/users.sqlite --manage-users -- \
    add writer "$BW_WRITER_PASSWORD"
fi
exec calibre-server /library \
  --port 8081 --listen-on 0.0.0.0 \
  --enable-auth --userdb /auth/users.sqlite \
  --disable-local-write --disable-use-bonjour --max-jobs 1
