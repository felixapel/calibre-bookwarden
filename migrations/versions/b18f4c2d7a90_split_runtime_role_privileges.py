"""split runtime role privileges

Revision ID: b18f4c2d7a90
Revises: 6a3f83d9e621
Create Date: 2026-07-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "b18f4c2d7a90"
down_revision: str | Sequence[str] | None = "6a3f83d9e621"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(
        """
DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_app')
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_writer') THEN
    REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM bookaudit_app, bookaudit_writer;
    REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM bookaudit_app, bookaudit_writer;

    GRANT SELECT ON ALL TABLES IN SCHEMA public TO bookaudit_app, bookaudit_writer;

    GRANT INSERT, UPDATE ON TABLE bookrecord, run, covervisioncache,
      verificationrun, verificationresult TO bookaudit_app;
    GRANT INSERT, UPDATE, DELETE ON TABLE evidencepackage TO bookaudit_app;
    GRANT INSERT ON TABLE manualauthorization, operationledger, outboxevent TO bookaudit_app;
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO bookaudit_app;

    GRANT UPDATE ON TABLE bookrecord, operationledger, outboxevent TO bookaudit_writer;
    GRANT INSERT, UPDATE ON TABLE "change" TO bookaudit_writer;
    GRANT INSERT, UPDATE, DELETE ON TABLE bookwritelock TO bookaudit_writer;
    GRANT USAGE, SELECT ON SEQUENCE change_id_seq TO bookaudit_writer;
  END IF;
END
$acl$;
"""
    )


def downgrade() -> None:
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(
        """
DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_app')
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_writer') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public
      TO bookaudit_app, bookaudit_writer;
    GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public
      TO bookaudit_app, bookaudit_writer;
  END IF;
END
$acl$;
"""
    )
