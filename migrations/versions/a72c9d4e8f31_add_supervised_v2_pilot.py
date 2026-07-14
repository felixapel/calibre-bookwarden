"""add supervised V2 pilot

Revision ID: a72c9d4e8f31
Revises: e91a7f42c6b4
Create Date: 2026-07-14
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel
from alembic import op

revision: str = "a72c9d4e8f31"
down_revision: str | Sequence[str] | None = "e91a7f42c6b4"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.create_table(
        "pilotsession",
        sa.Column("pilot_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("library_root_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("release_digest", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("alembic_revision", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("max_operations", sa.Integer(), nullable=False),
        sa.Column("reserved_operations", sa.Integer(), nullable=False),
        sa.Column("state", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("created_at", sa.DateTime(), nullable=False),
        sa.Column("updated_at", sa.DateTime(), nullable=False),
        sa.CheckConstraint("max_operations BETWEEN 1 AND 5", name="ck_pilotsession_max_operations"),
        sa.CheckConstraint(
            "reserved_operations BETWEEN 0 AND max_operations",
            name="ck_pilotsession_reserved_operations",
        ),
        sa.CheckConstraint(
            "state IN ('open', 'stopped', 'completed')",
            name="ck_pilotsession_state",
        ),
        sa.PrimaryKeyConstraint("pilot_id"),
    )
    op.create_index(
        op.f("ix_pilotsession_library_root_sha256"),
        "pilotsession",
        ["library_root_sha256"],
        unique=False,
    )
    op.create_index(op.f("ix_pilotsession_state"), "pilotsession", ["state"], unique=False)
    op.add_column(
        "operationledger",
        sa.Column("pilot_id", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
    )
    op.create_index(op.f("ix_operationledger_pilot_id"), "operationledger", ["pilot_id"], unique=False)
    op.create_table(
        "operationincidentacknowledgement",
        sa.Column("operation_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("actor", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("reason", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("created_at", sa.DateTime(), nullable=False),
        sa.ForeignKeyConstraint(
            ["operation_id"],
            ["operationledger.operation_id"],
            name="fk_incident_ack_operation",
        ),
        sa.CheckConstraint(
            "length(trim(actor)) BETWEEN 1 AND 128",
            name="ck_incident_ack_actor_length",
        ),
        sa.CheckConstraint(
            "length(trim(reason)) BETWEEN 12 AND 1000",
            name="ck_incident_ack_reason_length",
        ),
        sa.PrimaryKeyConstraint("operation_id"),
    )

    if op.get_bind().dialect.name == "postgresql":
        op.execute(
            """
DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_app')
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bookaudit_writer') THEN
    GRANT SELECT, INSERT, UPDATE ON TABLE pilotsession TO bookaudit_app;
    GRANT SELECT ON TABLE pilotsession TO bookaudit_writer;
    GRANT SELECT, INSERT ON TABLE operationincidentacknowledgement TO bookaudit_app;
    GRANT SELECT ON TABLE operationincidentacknowledgement TO bookaudit_writer;
  END IF;
END
$acl$;
"""
        )


def downgrade() -> None:
    bind = op.get_bind()
    acknowledgement = bind.execute(sa.text("SELECT 1 FROM operationincidentacknowledgement LIMIT 1")).first()
    active_pilot = bind.execute(sa.text("SELECT 1 FROM pilotsession LIMIT 1")).first()
    bound_operation = bind.execute(sa.text("SELECT 1 FROM operationledger WHERE pilot_id IS NOT NULL LIMIT 1")).first()
    if acknowledgement is not None or active_pilot is not None or bound_operation is not None:
        raise RuntimeError("refusing to drop persisted supervised V2 pilot evidence")
    op.drop_table("operationincidentacknowledgement")
    op.drop_index(op.f("ix_operationledger_pilot_id"), table_name="operationledger")
    op.drop_column("operationledger", "pilot_id")
    op.drop_index(op.f("ix_pilotsession_state"), table_name="pilotsession")
    op.drop_index(op.f("ix_pilotsession_library_root_sha256"), table_name="pilotsession")
    op.drop_table("pilotsession")
