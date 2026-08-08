"""add Certificate A verifier request and fencing contract

Revision ID: e3c1a4b7d902
Revises: c8e1f0a2b4d6
Create Date: 2026-08-08
"""

from collections.abc import Sequence

import sqlalchemy as sa
import sqlmodel
from alembic import op

revision: str = "e3c1a4b7d902"
down_revision: str | Sequence[str] | None = "c8e1f0a2b4d6"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    with op.batch_alter_table("verificationrun") as batch:
        batch.add_column(sa.Column("contract_version", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
        batch.add_column(sa.Column("idempotency_key", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
        batch.add_column(sa.Column("request_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
        batch.add_column(sa.Column("source_root", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
        batch.add_column(sa.Column("source_root_sha256", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
        batch.add_column(sa.Column("effective_config", sa.JSON(), nullable=True))
        batch.add_column(sa.Column("source_snapshot", sa.JSON(), nullable=True))
        batch.add_column(sa.Column("fence_token", sa.Integer(), server_default=sa.text("0"), nullable=False))
        batch.add_column(sa.Column("claimed_at", sa.DateTime(), nullable=True))
        batch.add_column(sa.Column("heartbeat_at", sa.DateTime(), nullable=True))
        batch.add_column(sa.Column("inventory_finished_at", sa.DateTime(), nullable=True))
        batch.add_column(sa.Column("cancel_requested_at", sa.DateTime(), nullable=True))
        batch.add_column(sa.Column("error_code", sqlmodel.sql.sqltypes.AutoString(), nullable=True))
        batch.create_unique_constraint(
            "uq_verificationrun_idempotency_key",
            ["idempotency_key"],
        )
        batch.create_check_constraint(
            "ck_verificationrun_fence_nonnegative",
            "fence_token >= 0",
        )

    # Historical in-flight V2 work has no immutable Certificate A run
    # contract. It must never be resumed under current process defaults.
    op.execute(
        sa.text(
            """
            UPDATE verificationrun
            SET status = 'blocked_recovery',
                finished_at = COALESCE(finished_at, CURRENT_TIMESTAMP),
                lease_owner = NULL,
                lease_expires_at = NULL,
                error_code = 'pre_certificate_a_contract'
            WHERE pipeline_version = 'manifestation-v2'
              AND (
                    finished_at IS NULL
                    OR status NOT IN (
                        'completed', 'completed_with_errors', 'failed',
                        'source_changed', 'cancelled', 'blocked_recovery'
                    )
                  )
            """
        )
    )

    op.create_index(
        op.f("ix_verificationrun_contract_version"),
        "verificationrun",
        ["contract_version"],
        unique=False,
    )
    op.create_index(
        op.f("ix_verificationrun_source_root_sha256"),
        "verificationrun",
        ["source_root_sha256"],
        unique=False,
    )
    op.create_index(
        op.f("ix_verificationrun_heartbeat_at"),
        "verificationrun",
        ["heartbeat_at"],
        unique=False,
    )
    op.create_index(
        op.f("ix_verificationrun_cancel_requested_at"),
        "verificationrun",
        ["cancel_requested_at"],
        unique=False,
    )
    active_predicate = sa.text(
        "contract_version = 'certificate-a-v1' AND status IN ('pending', 'inventorying', 'running', 'cancelling')"
    )
    op.create_index(
        "uq_verificationrun_active_source",
        "verificationrun",
        ["source_root_sha256"],
        unique=True,
        postgresql_where=active_predicate,
        sqlite_where=active_predicate,
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
            WHERE contract_version IS NOT NULL
               OR idempotency_key IS NOT NULL
               OR request_sha256 IS NOT NULL
               OR source_root IS NOT NULL
               OR source_root_sha256 IS NOT NULL
               OR source_snapshot IS NOT NULL
               OR fence_token != 0
               OR claimed_at IS NOT NULL
               OR heartbeat_at IS NOT NULL
               OR inventory_finished_at IS NOT NULL
               OR cancel_requested_at IS NOT NULL
               OR error_code = 'pre_certificate_a_contract'
            LIMIT 1
            """
        )
    ).first()
    if unsafe_run is not None:
        raise RuntimeError("refusing to drop Certificate A verifier state while contracted or quarantined runs exist")

    op.drop_index("uq_verificationrun_active_source", table_name="verificationrun")
    op.drop_index(op.f("ix_verificationrun_cancel_requested_at"), table_name="verificationrun")
    op.drop_index(op.f("ix_verificationrun_heartbeat_at"), table_name="verificationrun")
    op.drop_index(op.f("ix_verificationrun_source_root_sha256"), table_name="verificationrun")
    op.drop_index(op.f("ix_verificationrun_contract_version"), table_name="verificationrun")
    with op.batch_alter_table("verificationrun") as batch:
        batch.drop_constraint("ck_verificationrun_fence_nonnegative", type_="check")
        batch.drop_constraint("uq_verificationrun_idempotency_key", type_="unique")
        batch.drop_column("error_code")
        batch.drop_column("cancel_requested_at")
        batch.drop_column("inventory_finished_at")
        batch.drop_column("heartbeat_at")
        batch.drop_column("claimed_at")
        batch.drop_column("fence_token")
        batch.drop_column("source_snapshot")
        batch.drop_column("effective_config")
        batch.drop_column("source_root_sha256")
        batch.drop_column("source_root")
        batch.drop_column("request_sha256")
        batch.drop_column("idempotency_key")
        batch.drop_column("contract_version")
