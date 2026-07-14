"""add manifestation v2 evidence persistence

Revision ID: d4c9a21e8f30
Revises: b18f4c2d7a90
Create Date: 2026-07-14
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel
from alembic import op

revision: str = "d4c9a21e8f30"
down_revision: str | Sequence[str] | None = "b18f4c2d7a90"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.add_column(
        "evidencepackage",
        sa.Column("schema_version", sa.Integer(), nullable=False, server_default="1"),
    )
    op.add_column("evidencepackage", sa.Column("observations", sa.JSON(), nullable=True))
    op.add_column(
        "verificationrun",
        sa.Column(
            "pipeline_version",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
            server_default="v1",
        ),
    )
    op.add_column(
        "verificationrun",
        sa.Column("mode", sqlmodel.sql.sqltypes.AutoString(), nullable=False, server_default="legacy"),
    )
    op.add_column(
        "verificationresult",
        sa.Column("evidence_id", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column(
        "verificationresult",
        sa.Column("state", sqlmodel.sql.sqltypes.AutoString(), nullable=False, server_default="pending"),
    )
    op.create_index(
        op.f("ix_verificationresult_evidence_id"),
        "verificationresult",
        ["evidence_id"],
        unique=False,
    )
    op.create_index(
        op.f("ix_verificationresult_state"),
        "verificationresult",
        ["state"],
        unique=False,
    )


def downgrade() -> None:
    bind = op.get_bind()
    evidence = bind.execute(sa.text("SELECT 1 FROM evidencepackage WHERE schema_version = 2 LIMIT 1")).first()
    runs = bind.execute(
        sa.text("SELECT 1 FROM verificationrun WHERE pipeline_version = 'manifestation-v2' LIMIT 1")
    ).first()
    if evidence is not None or runs is not None:
        raise RuntimeError("refusing to drop persisted Manifestation V2 audit evidence")
    op.drop_index(op.f("ix_verificationresult_state"), table_name="verificationresult")
    op.drop_index(op.f("ix_verificationresult_evidence_id"), table_name="verificationresult")
    op.drop_column("verificationresult", "state")
    op.drop_column("verificationresult", "evidence_id")
    op.drop_column("verificationrun", "mode")
    op.drop_column("verificationrun", "pipeline_version")
    op.drop_column("evidencepackage", "observations")
    op.drop_column("evidencepackage", "schema_version")
