"""isolate Certificate A runtime roles

Revision ID: f4a2d6e8c013
Revises: e3c1a4b7d902
Create Date: 2026-08-08
"""

from collections.abc import Sequence

from alembic import op

revision: str = "f4a2d6e8c013"
down_revision: str | Sequence[str] | None = "e3c1a4b7d902"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(
        """
DO $acl$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_app')
     OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_verifier')
     OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_writer') THEN
    RAISE EXCEPTION 'bookaudit_app, bookaudit_verifier, and bookaudit_writer roles must be provisioned first';
  END IF;

  REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public
    FROM bookaudit_app, bookaudit_verifier;
  REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public
    FROM bookaudit_app, bookaudit_verifier;

  GRANT SELECT ON TABLE alembic_version, verificationrun,
    verificationresult, evidencepackage TO bookaudit_app;
  GRANT INSERT ON TABLE verificationrun TO bookaudit_app;
  GRANT UPDATE (status, cancel_requested_at) ON TABLE verificationrun TO bookaudit_app;
  GRANT USAGE, SELECT ON SEQUENCE verificationrun_id_seq TO bookaudit_app;

  GRANT SELECT ON TABLE alembic_version, bookrecord, evidencepackage,
    verificationrun, verificationresult TO bookaudit_verifier;
  GRANT INSERT, UPDATE ON TABLE bookrecord, verificationresult TO bookaudit_verifier;
  GRANT INSERT ON TABLE evidencepackage TO bookaudit_verifier;
  GRANT UPDATE ON TABLE verificationrun TO bookaudit_verifier;
  GRANT USAGE, SELECT ON SEQUENCE bookrecord_id_seq, evidencepackage_id_seq,
    verificationresult_id_seq TO bookaudit_verifier;
END
$acl$;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
  REVOKE ALL ON TABLES FROM bookaudit_app, bookaudit_verifier;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  REVOKE ALL ON SEQUENCES FROM bookaudit_app, bookaudit_verifier;
"""
    )


def downgrade() -> None:
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(
        """
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public
  FROM bookaudit_app, bookaudit_verifier;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public
  FROM bookaudit_app, bookaudit_verifier;

GRANT SELECT ON ALL TABLES IN SCHEMA public TO bookaudit_app;
GRANT INSERT, UPDATE ON TABLE bookrecord, run, covervisioncache,
  verificationrun, verificationresult TO bookaudit_app;
GRANT INSERT, UPDATE, DELETE ON TABLE evidencepackage TO bookaudit_app;
GRANT INSERT ON TABLE manualauthorization, operationledger, outboxevent TO bookaudit_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO bookaudit_app;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO bookaudit_app;
"""
    )
