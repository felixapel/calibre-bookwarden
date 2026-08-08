#!/usr/bin/env bash
set -euo pipefail

: "${POSTGRES_APP_PASSWORD:?POSTGRES_APP_PASSWORD is required}"
: "${POSTGRES_VERIFIER_PASSWORD:?POSTGRES_VERIFIER_PASSWORD is required}"
: "${POSTGRES_WRITER_PASSWORD:?POSTGRES_WRITER_PASSWORD is required}"
: "${POSTGRES_MIGRATOR_PASSWORD:?POSTGRES_MIGRATOR_PASSWORD is required}"

psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set=app_password="$POSTGRES_APP_PASSWORD" \
  --set=verifier_password="$POSTGRES_VERIFIER_PASSWORD" \
  --set=writer_password="$POSTGRES_WRITER_PASSWORD" \
  --set=migrator_password="$POSTGRES_MIGRATOR_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE bookaudit_app LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'bookaudit_app') \gexec
SELECT format('ALTER ROLE bookaudit_app PASSWORD %L', :'app_password') \gexec

SELECT format('CREATE ROLE bookaudit_verifier LOGIN PASSWORD %L', :'verifier_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'bookaudit_verifier') \gexec
SELECT format('ALTER ROLE bookaudit_verifier PASSWORD %L', :'verifier_password') \gexec

SELECT format('CREATE ROLE bookaudit_writer LOGIN PASSWORD %L', :'writer_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'bookaudit_writer') \gexec
SELECT format('ALTER ROLE bookaudit_writer PASSWORD %L', :'writer_password') \gexec

SELECT format('CREATE ROLE bookaudit_migrator LOGIN PASSWORD %L', :'migrator_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'bookaudit_migrator') \gexec
SELECT format('ALTER ROLE bookaudit_migrator PASSWORD %L', :'migrator_password') \gexec

SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format(
  'GRANT CONNECT ON DATABASE %I TO bookaudit_app, bookaudit_verifier, bookaudit_writer, bookaudit_migrator',
  current_database()
) \gexec
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO bookaudit_migrator;
GRANT USAGE ON SCHEMA public TO bookaudit_app, bookaudit_verifier, bookaudit_writer;

ALTER DEFAULT PRIVILEGES FOR ROLE bookaudit_migrator IN SCHEMA public
  REVOKE ALL ON TABLES FROM bookaudit_app, bookaudit_verifier, bookaudit_writer;
ALTER DEFAULT PRIVILEGES FOR ROLE bookaudit_migrator IN SCHEMA public
  REVOKE ALL ON SEQUENCES FROM bookaudit_app, bookaudit_verifier, bookaudit_writer;
SQL
