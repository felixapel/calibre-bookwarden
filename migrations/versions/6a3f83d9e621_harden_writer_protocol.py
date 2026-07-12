"""harden writer protocol

Revision ID: 6a3f83d9e621
Revises: f6369c0a2137
Create Date: 2026-07-12
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel
from alembic import op

revision: str = "6a3f83d9e621"
down_revision: str | Sequence[str] | None = "f6369c0a2137"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.add_column("change", sa.Column("operation_id", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
    op.add_column("change", sa.Column("backup_opf_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
    op.create_index(op.f("ix_change_operation_id"), "change", ["operation_id"], unique=True)

    for _name, column in (
        ("calibre_book_id", sa.Column("calibre_book_id", sa.Integer(), nullable=True)),
        ("expected_before_metadata", sa.Column("expected_before_metadata", sa.JSON(), nullable=True)),
        ("verdict_hash", sa.Column("verdict_hash", sqlmodel.sql.sqltypes.AutoString(), nullable=True)),
        ("patch_hash", sa.Column("patch_hash", sqlmodel.sql.sqltypes.AutoString(), nullable=True)),
        ("field_locks_hash", sa.Column("field_locks_hash", sqlmodel.sql.sqltypes.AutoString(), nullable=True)),
        (
            "policy_version",
            sa.Column("policy_version", sqlmodel.sql.sqltypes.AutoString(), nullable=False, server_default="v1"),
        ),
        ("change_id", sa.Column("change_id", sa.Integer(), nullable=True)),
        ("rollback_opf_path", sa.Column("rollback_opf_path", sqlmodel.sql.sqltypes.AutoString(), nullable=True)),
        ("rollback_opf_sha256", sa.Column("rollback_opf_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=True)),
        ("lease_owner", sa.Column("lease_owner", sqlmodel.sql.sqltypes.AutoString(), nullable=True)),
        ("lease_expires_at", sa.Column("lease_expires_at", sa.DateTime(), nullable=True)),
    ):
        op.add_column("operationledger", column)
    for name in ("calibre_book_id", "change_id", "lease_owner", "lease_expires_at"):
        op.create_index(op.f(f"ix_operationledger_{name}"), "operationledger", [name], unique=False)

    op.create_table(
        "bookwritelock",
        sa.Column("book_key", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("operation_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("lease_owner", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("lease_expires_at", sa.DateTime(), nullable=False),
        sa.PrimaryKeyConstraint("book_key"),
    )
    op.create_index(op.f("ix_bookwritelock_operation_id"), "bookwritelock", ["operation_id"], unique=True)
    op.create_index(op.f("ix_bookwritelock_lease_owner"), "bookwritelock", ["lease_owner"], unique=False)
    op.create_index(op.f("ix_bookwritelock_lease_expires_at"), "bookwritelock", ["lease_expires_at"], unique=False)


def downgrade() -> None:
    op.drop_table("bookwritelock")
    for name in ("lease_expires_at", "lease_owner", "change_id", "calibre_book_id"):
        op.drop_index(op.f(f"ix_operationledger_{name}"), table_name="operationledger")
    for name in (
        "lease_expires_at",
        "lease_owner",
        "rollback_opf_sha256",
        "rollback_opf_path",
        "change_id",
        "policy_version",
        "field_locks_hash",
        "patch_hash",
        "verdict_hash",
        "calibre_book_id",
        "expected_before_metadata",
    ):
        op.drop_column("operationledger", name)
    op.drop_index(op.f("ix_change_operation_id"), table_name="change")
    op.drop_column("change", "backup_opf_sha256")
    op.drop_column("change", "operation_id")
