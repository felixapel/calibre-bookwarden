"""add verification run worker lease

Revision ID: c8e1f0a2b4d6
Revises: a72c9d4e8f31
Create Date: 2026-07-17
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel
from alembic import op

revision: str = "c8e1f0a2b4d6"
down_revision: str | Sequence[str] | None = "a72c9d4e8f31"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.add_column(
        "verificationrun",
        sa.Column("lease_owner", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column(
        "verificationrun",
        sa.Column("lease_expires_at", sa.DateTime(), nullable=True),
    )
    op.create_index(op.f("ix_verificationrun_lease_owner"), "verificationrun", ["lease_owner"], unique=False)
    op.create_index(
        op.f("ix_verificationrun_lease_expires_at"),
        "verificationrun",
        ["lease_expires_at"],
        unique=False,
    )


def downgrade() -> None:
    bind = op.get_bind()
    if bind.dialect.name == "postgresql":
        bind.execute(sa.text("LOCK TABLE verificationrun IN ACCESS EXCLUSIVE MODE NOWAIT"))
    unsafe_run = bind.execute(
        sa.text(
            """
            SELECT 1
            FROM verificationrun
            WHERE lease_owner IS NOT NULL
               OR lease_expires_at IS NOT NULL
               OR (
                    pipeline_version = 'manifestation-v2'
                    AND (
                         finished_at IS NULL
                         OR status NOT IN ('completed', 'completed_with_errors', 'failed', 'blocked_recovery')
                    )
              )
            LIMIT 1
            """
        )
    ).first()
    if unsafe_run is not None:
        raise RuntimeError(
            "refusing to drop verification run lease state while leased or non-terminal Manifestation V2 runs exist"
        )
    op.drop_index(op.f("ix_verificationrun_lease_expires_at"), table_name="verificationrun")
    op.drop_index(op.f("ix_verificationrun_lease_owner"), table_name="verificationrun")
    op.drop_column("verificationrun", "lease_expires_at")
    op.drop_column("verificationrun", "lease_owner")
