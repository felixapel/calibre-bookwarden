"""harden v2 writer artifacts

Revision ID: e91a7f42c6b4
Revises: d4c9a21e8f30
Create Date: 2026-07-14
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel
from alembic import op

revision: str = "e91a7f42c6b4"
down_revision: str | Sequence[str] | None = "d4c9a21e8f30"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.add_column(
        "change",
        sa.Column("backup_cover_path", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column(
        "change",
        sa.Column("backup_cover_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column("change", sa.Column("before_custom", sa.JSON(), nullable=True))
    op.add_column(
        "operationledger",
        sa.Column("evidence_id", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column(
        "operationledger",
        sa.Column("rollback_cover_path", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column(
        "operationledger",
        sa.Column("rollback_cover_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.add_column("operationledger", sa.Column("rollback_custom", sa.JSON(), nullable=True))
    op.create_index(
        op.f("ix_operationledger_evidence_id"),
        "operationledger",
        ["evidence_id"],
        unique=False,
    )


def downgrade() -> None:
    bind = op.get_bind()
    active_v2 = bind.execute(
        sa.text(
            "SELECT 1 FROM operationledger WHERE policy_version = 'manifestation-v2' OR evidence_id IS NOT NULL LIMIT 1"
        )
    ).first()
    cover_recovery = bind.execute(
        sa.text("SELECT 1 FROM change WHERE backup_cover_path IS NOT NULL OR before_custom IS NOT NULL LIMIT 1")
    ).first()
    if active_v2 is not None or cover_recovery is not None:
        raise RuntimeError("refusing to drop active V2 writer or recovery evidence")
    op.drop_index(op.f("ix_operationledger_evidence_id"), table_name="operationledger")
    op.drop_column("operationledger", "rollback_custom")
    op.drop_column("operationledger", "rollback_cover_sha256")
    op.drop_column("operationledger", "rollback_cover_path")
    op.drop_column("operationledger", "evidence_id")
    op.drop_column("change", "before_custom")
    op.drop_column("change", "backup_cover_sha256")
    op.drop_column("change", "backup_cover_path")
