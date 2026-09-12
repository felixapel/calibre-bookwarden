import hashlib
from datetime import timedelta
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import (
    PilotGuard,
    create_v2_manual_authorization,
    queue_approved_operations,
    queue_undo_operation,
    queue_v2_operation,
)
from calibre_ai_auditor.apply.writer import MetadataWriter, claim_next_operation, reconcile_incomplete_operations
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import (
    BookRecord,
    BookWriteLock,
    Change,
    EvidencePackage,
    OperationLedger,
    OutboxEvent,
    utc_now,
)
from calibre_ai_auditor.storage.operations import create_operation, transition_operation
from tests.v2_fixtures import build_exact_tier_a_package


def _writer_pilot(library_root: Path | str) -> PilotGuard:
    return PilotGuard(
        enabled=True,
        pilot_id="writer-test-pilot",
        library_root=str(library_root),
        release_digest=f"sha256:{'d' * 64}",
        alembic_revision=expected_schema_revision(),
        max_operations=5,
        writer_ready=True,
    )


def _setup_operation(session: Session) -> str:
    session.add(
        BookRecord(
            book_key="calibre:1",
            run_id="run-1",
            calibre_book_id=1,
            current_metadata={"title": "Old"},
            status="suggest_fix",
        )
    )
    session.add(
        EvidencePackage(
            evidence_id="evidence-1",
            book_key="calibre:1",
            run_id="run-1",
            decision={
                "book_key": "calibre:1",
                "run_id": "run-1",
                "action": "suggest_fix",
                "auto_apply_eligible": True,
                "overall_confidence": 99,
                "proposed_patch": {"title": "New"},
            },
        )
    )
    session.commit()
    result = queue_approved_operations(session, book_keys=["calibre:1"], authorization_ids={})
    return result.queued_operation_ids[0]


def _setup_v2_operation(
    session: Session,
    ebook: Path,
    *,
    additional_ebooks: list[Path] | None = None,
    current_metadata: dict[str, object] | None = None,
    patch: dict[str, object] | None = None,
    snapshot_library_root: Path | None = None,
) -> str:
    ebooks = [ebook, *(additional_ebooks or [])]
    digests = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in ebooks}
    current = current_metadata or {"title": "Old"}
    requested_patch = patch or {"title": "New"}
    package = build_exact_tier_a_package(
        evidence_id="evidence-v2-writer",
        run_id="run-v2-writer",
        book_id=11,
        library_root=str(snapshot_library_root or ebook.parent),
        files=[str(path) for path in ebooks],
        current_metadata=current,
        resolved_patch=requested_patch,
        file_sha256=digests,
    )
    book = BookRecord(
        book_key=package.book_key,
        run_id=package.run_id,
        calibre_book_id=11,
        current_metadata=package.snapshot.current_metadata,
        files=[{"path": str(path), "format": path.suffix.removeprefix(".").upper()} for path in ebooks],
        status="shadowed",
    )
    session.add(book)
    session.add(
        EvidencePackage(
            evidence_id=package.evidence_id,
            book_key=package.book_key,
            run_id=package.run_id,
            schema_version=2,
            current=package.snapshot.current_metadata,
            extracted=package.identity.model_dump(mode="json"),
            observations=package.model_dump(mode="json"),
        )
    )
    session.commit()
    authorization = create_v2_manual_authorization(
        session,
        book=book,
        package=package,
        actor="operator",
        reason="Exact manifestation reviewed",
    )
    session.commit()
    return queue_v2_operation(
        session,
        evidence_id=package.evidence_id,
        authorization_id=authorization.authorization_id,
        pilot=_writer_pilot(package.snapshot.library_root or ""),
    )


def test_v2_writer_rejects_ebook_changed_after_authorization(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        ebook.write_bytes(b"different edition")
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.return_value = {"title": "Old", "formats": [str(ebook)]}
        apply_engine = MagicMock()

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "failed"
        assert "ebook snapshot changed" in (operation.error or "")
        apply_engine.apply_patch.assert_not_called()


def test_v2_writer_refuses_to_read_calibre_without_runtime_pilot_binding(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library

        operation = MetadataWriter(cli, MagicMock()).process(session, operation_id)

        assert operation.state == "failed"
        assert "runtime pilot binding" in (operation.error or "")
        cli.show_metadata.assert_not_called()


def test_v2_writer_rejects_configured_library_different_from_sealed_root(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook, snapshot_library_root=tmp_path)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.return_value = {"title": "Old", "formats": [str(ebook)]}
        apply_engine = MagicMock()

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(tmp_path)).process(session, operation_id)

        assert operation.state == "failed"
        assert "library differs" in (operation.error or "")
        apply_engine.apply_patch.assert_not_called()


def test_v2_writer_rolls_back_metadata_if_ebook_changes_during_write(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    backup = tmp_path / "before.opf"
    backup.write_text("backup")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.side_effect = [
            {"title": "Old", "formats": [str(ebook)]},
            {"title": "New", "formats": [str(ebook)]},
            {"title": "Old", "formats": [str(ebook)]},
        ]
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path

        def mutate_ebook(*_args: object, **_kwargs: object) -> Change:
            ebook.write_bytes(b"unexpected replacement")
            return Change(
                operation_id=operation_id,
                book_key="calibre:11",
                run_id="run-v2-writer",
                backup_opf_path=str(backup),
                backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
            )

        apply_engine.apply_patch.side_effect = mutate_ebook

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "restored"
        assert "ebook changed during metadata write" in (operation.error or "")
        cli.set_metadata.assert_called_once_with(11, backup)


def test_v2_writer_normalizes_edition_alias_in_target_readback(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(
            session,
            ebook,
            current_metadata={"title": "Old", "edition_statement": "First edition"},
            patch={"edition_statement": "Second edition"},
        )
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        before = {"title": "Old", "#edition": "First edition", "formats": [str(ebook)]}
        after = {"title": "Old", "#edition": "Second edition", "formats": [str(ebook)]}
        cli.show_metadata.side_effect = [before, after, after]
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:11",
            run_id="run-v2-writer",
            backup_opf_path=str(tmp_path / "before.opf"),
        )

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "succeeded"
        assert operation.target_metadata is not None
        assert "#edition" not in operation.target_metadata
        assert operation.target_metadata["edition_statement"] == "Second edition"


def test_writer_verifies_successful_target(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        before = {
            "title": "Old",
            "formats": [str(ebook)],
            "path": "Old",
            "last_modified": "before-write",
        }
        relocated = library / "New" / "book.epub"
        after = {
            "title": "New",
            "formats": [str(relocated)],
            "path": "New",
            "last_modified": "after-write",
        }
        calls = 0

        def show_metadata(_book_id: int) -> dict[str, object]:
            nonlocal calls
            calls += 1
            if calls == 1:
                return before
            if ebook.exists():
                relocated.parent.mkdir()
                ebook.rename(relocated)
            return after

        cli.show_metadata.side_effect = show_metadata
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:11",
            run_id="run-v2-writer",
            backup_opf_path=str(tmp_path / "before.opf"),
        )

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "succeeded"
        cli.set_metadata.assert_not_called()


def test_writer_restores_when_write_corrupts_unpatched_metadata(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(
            session,
            ebook,
            current_metadata={"title": "Old", "publisher": "Expected Publisher"},
        )
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        before = {
            "title": "Old",
            "publisher": "Expected Publisher",
            "formats": [str(ebook)],
            "last_modified": "before-write",
        }
        corrupted = {
            "title": "New",
            "publisher": "Corrupted Publisher",
            "formats": [str(ebook)],
            "last_modified": "after-write",
        }
        restored = {**before, "last_modified": "after-restore"}
        cli.show_metadata.side_effect = [before, corrupted, corrupted, restored]
        backup = tmp_path / "before.opf"
        backup.write_text("backup")
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:11",
            run_id="run-v2-writer",
            backup_opf_path=str(backup),
            backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
        )

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(11, backup)


def test_v2_writer_rejects_duplicate_live_paths_after_relocation(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    first = library / "first.epub"
    second = library / "second.epub"
    first.write_bytes(b"same authorized edition")
    second.write_bytes(b"same authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, first, additional_ebooks=[second])
        assert claim_next_operation(session) == operation_id
        duplicate = library / "New" / "book.epub"
        duplicate.parent.mkdir()
        duplicate.write_bytes(first.read_bytes())
        before = {"title": "Old", "formats": [str(first), str(second)]}
        after = {"title": "New", "formats": [str(duplicate), str(duplicate)]}
        restored = {"title": "Old", "formats": [str(first), str(second)]}
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.side_effect = [before, after, restored]
        backup = tmp_path / "before.opf"
        backup.write_text("backup")
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:11",
            run_id="run-v2-writer",
            backup_opf_path=str(backup),
            backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
        )

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "restored"
        assert "ebook changed during metadata write" in (operation.error or "")
        cli.set_metadata.assert_called_once_with(11, backup)


def test_writer_refuses_legacy_apply_before_reading_calibre() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        apply_engine = MagicMock()

        operation = MetadataWriter(cli, apply_engine).process(session, operation_id)

        assert operation.state == "failed"
        assert "legacy metadata apply is disabled" in (operation.error or "")
        cli.show_metadata.assert_not_called()
        apply_engine.apply_patch.assert_not_called()


def test_writer_restores_verified_partial_write(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        before = {"title": "Old", "formats": [str(ebook)], "last_modified": "before-write"}
        partial = {"title": "Partial", "formats": [str(ebook)]}
        restored = {"title": "Old", "formats": [str(ebook)], "last_modified": "after-restore"}
        cli.show_metadata.side_effect = [before, partial, partial, restored]
        backup = tmp_path / "before.opf"
        backup.write_text("backup")
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:11",
            run_id="run-v2-writer",
            backup_opf_path=str(backup),
            backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
        )

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(11, backup)


def test_writer_marks_restore_failed_when_backup_does_not_restore_before_state(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        before = {"title": "Old", "formats": [str(ebook)]}
        partial = {"title": "Partial", "formats": [str(ebook)]}
        still_partial = {"title": "Still Partial", "formats": [str(ebook)]}
        cli.show_metadata.side_effect = [before, partial, partial, still_partial]
        backup = tmp_path / "wrong-restore.opf"
        backup.write_text("backup")
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:11",
            run_id="run-v2-writer",
            backup_opf_path=str(backup),
            backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
        )

        operation = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert operation.state == "restore_failed"


def test_reconciliation_restores_partial_crash_state(tmp_path: Path) -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.before_metadata = {"title": "Old"}
        operation.target_metadata = {"title": "New"}
        transition_operation(operation, "writing")
        session.add(operation)
        backup = tmp_path / "before.opf"
        backup.write_text("verified backup")
        session.add(
            Change(
                operation_id=operation_id,
                book_key="calibre:1",
                run_id="run-1",
                backup_opf_path=str(backup),
                backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
                status="pending_apply",
            )
        )
        session.commit()
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Partial"}, {"title": "Old"}]

        reconciled = reconcile_incomplete_operations(session, cli, tmp_path)

        session.refresh(operation)
        assert reconciled == [operation_id]
        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(1, backup)


def test_reconciliation_restores_when_unpatched_metadata_is_corrupted(tmp_path: Path) -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.before_metadata = {
            "title": "Old",
            "publisher": "Expected Publisher",
            "last_modified": "before-write",
        }
        operation.target_metadata = {
            "title": "New",
            "publisher": "Expected Publisher",
            "last_modified": "before-write",
        }
        transition_operation(operation, "writing")
        session.add(operation)
        backup = tmp_path / "before.opf"
        backup.write_text("verified backup")
        session.add(
            Change(
                operation_id=operation_id,
                book_key="calibre:1",
                run_id="run-1",
                backup_opf_path=str(backup),
                backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
                status="pending_apply",
            )
        )
        session.commit()
        cli = MagicMock()
        cli.show_metadata.side_effect = [
            {
                "title": "New",
                "publisher": "Corrupted Publisher",
                "last_modified": "after-write",
            },
            {
                "title": "Old",
                "publisher": "Expected Publisher",
                "last_modified": "after-restore",
            },
        ]

        reconcile_incomplete_operations(session, cli, tmp_path)

        session.refresh(operation)
        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(1, backup)


def test_v2_reconciliation_accepts_verified_calibre_relocation(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(
            session,
            ebook,
            current_metadata={"title": "Old", "publisher": "Expected Publisher"},
        )
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.before_metadata = {
            "title": "Old",
            "publisher": "Expected Publisher",
            "formats": [str(ebook)],
            "path": "Old",
            "last_modified": "before-write",
        }
        operation.target_metadata = {
            **operation.before_metadata,
            "title": "New",
        }
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()
        relocated = library / "New" / "book.epub"
        relocated.parent.mkdir()
        ebook.rename(relocated)
        observed = {
            "title": "New",
            "publisher": "Expected Publisher",
            "formats": [str(relocated)],
            "path": "New",
            "last_modified": "after-write",
        }
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.return_value = observed

        reconciled = reconcile_incomplete_operations(session, cli, tmp_path)

        session.refresh(operation)
        assert reconciled == [operation_id]
        assert operation.state == "succeeded"
        assert operation.target_metadata == {**observed, "last_modified": "before-write"}
        cli.set_metadata.assert_not_called()


def test_v2_reconciliation_restores_relocated_write_with_corrupted_metadata(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(
            session,
            ebook,
            current_metadata={"title": "Old", "publisher": "Expected Publisher"},
        )
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.before_metadata = {
            "title": "Old",
            "publisher": "Expected Publisher",
            "formats": [str(ebook)],
            "path": "Old",
        }
        operation.target_metadata = {**operation.before_metadata, "title": "New"}
        transition_operation(operation, "writing")
        session.add(operation)
        backup = tmp_path / "before.opf"
        backup.write_text("verified backup")
        session.add(
            Change(
                operation_id=operation_id,
                book_key="calibre:11",
                run_id="run-v2-writer",
                backup_opf_path=str(backup),
                backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
                status="pending_apply",
            )
        )
        session.commit()
        relocated = library / "New" / "book.epub"
        relocated.parent.mkdir()
        ebook.rename(relocated)
        corrupted = {
            "title": "New",
            "publisher": "Corrupted Publisher",
            "formats": [str(relocated)],
            "path": "New",
        }
        restored = {
            "title": "Old",
            "publisher": "Expected Publisher",
            "formats": [str(ebook)],
            "path": "Old",
        }
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.side_effect = [corrupted, restored]

        reconcile_incomplete_operations(session, cli, tmp_path)

        session.refresh(operation)
        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(11, backup)


def test_reconciliation_requeues_claimed_operation_before_any_write() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.lease_expires_at = utc_now() - timedelta(seconds=1)
        session.add(operation)
        session.commit()

        reconciled = reconcile_incomplete_operations(session, MagicMock())

        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
        assert reconciled == [operation_id]
        assert operation.state == "requested"
        assert event.status == "pending"
        assert claim_next_operation(session) == operation_id


def test_claim_skips_stale_event_and_claims_next_requested_operation() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        stale_id = _setup_operation(session)
        assert claim_next_operation(session) == stale_id
        stale = session.exec(select(OperationLedger).where(OperationLedger.operation_id == stale_id)).one()
        transition_operation(stale, "failed", error="invalid request")
        stale_event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == stale_id)).one()
        stale_event.status = "pending"
        session.add(stale)
        session.add(stale_event)
        second = create_operation(
            session,
            idempotency_key="apply:run-1:calibre-2:title",
            operation_type="apply_metadata",
            book_key="calibre:2",
            run_id="run-1",
            requested_patch={"title": "Second"},
        )
        session.commit()

        assert claim_next_operation(session) == second.operation_id


def test_writer_refuses_patch_tampered_after_queueing(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.requested_patch = {"title": "Injected"}
        session.add(operation)
        session.commit()
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()

        result = MetadataWriter(cli, MagicMock(), pilot=_writer_pilot(library)).process(session, operation_id)

        assert result.state == "failed"
        assert "evidence seal" in (result.error or "")
        cli.show_metadata.assert_not_called()


def test_writer_refuses_stale_patch_after_external_calibre_edit(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ebook = library / "book.epub"
    ebook.write_bytes(b"authorized edition")
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_v2_operation(session, ebook)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.library_path = library
        cli.show_metadata.return_value = {"title": "Newer Manual Edit", "formats": [str(ebook)]}
        apply_engine = MagicMock()

        result = MetadataWriter(cli, apply_engine, pilot=_writer_pilot(library)).process(session, operation_id)

        assert result.state == "failed"
        assert "changed after authorization" in (result.error or "")
        apply_engine.apply_patch.assert_not_called()


def test_writer_undo_verifies_target_and_updates_linked_change(tmp_path: Path) -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    original = tmp_path / "original.opf"
    original.write_text("original")
    with Session(engine) as session:
        book = BookRecord(
            book_key="calibre:7",
            run_id="run-7",
            calibre_book_id=7,
            current_metadata={"title": "Original"},
            status="applied",
        )
        change = Change(
            book_key=book.book_key,
            run_id=book.run_id,
            before_metadata={"title": "Original"},
            after_metadata={"title": "Applied"},
            backup_opf_path=str(original),
            backup_opf_sha256=hashlib.sha256(original.read_bytes()).hexdigest(),
            status="applied",
        )
        session.add(book)
        session.add(change)
        session.commit()
        operation_id = queue_undo_operation(session, change)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Applied"}, {"title": "Original"}]
        cli.export_opf.side_effect = lambda _book_id, path: path.write_text("applied")
        apply_engine = MagicMock()
        apply_engine.artifacts_dir = tmp_path

        operation = MetadataWriter(cli, apply_engine).process(session, operation_id)

        session.refresh(change)
        assert operation.state == "succeeded"
        assert change.status == "undone"
        cli.set_metadata.assert_called_once_with(7, original)


def test_undo_reconciliation_restores_pre_undo_state_after_partial_write(tmp_path: Path) -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    original = tmp_path / "original.opf"
    original.write_text("original")
    current = tmp_path / "current.opf"
    current.write_text("applied")
    with Session(engine) as session:
        book = BookRecord(book_key="calibre:8", run_id="run-8", calibre_book_id=8, status="applied")
        change = Change(
            book_key=book.book_key,
            run_id=book.run_id,
            before_metadata={"title": "Original"},
            after_metadata={"title": "Applied"},
            backup_opf_path=str(original),
            backup_opf_sha256=hashlib.sha256(original.read_bytes()).hexdigest(),
            status="applied",
        )
        session.add(book)
        session.add(change)
        session.commit()
        operation_id = queue_undo_operation(session, change)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.before_metadata = {"title": "Applied"}
        operation.target_metadata = {"title": "Original"}
        operation.change_id = change.id
        operation.rollback_opf_path = str(current)
        operation.rollback_opf_sha256 = hashlib.sha256(current.read_bytes()).hexdigest()
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Partial"}, {"title": "Applied"}]

        reconcile_incomplete_operations(session, cli, tmp_path)

        session.refresh(operation)
        session.refresh(change)
        assert operation.state == "restored"
        assert change.status == "applied"
        cli.set_metadata.assert_called_once_with(8, current)


def test_undo_reconciliation_terminalizes_missing_recovery_evidence() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book = BookRecord(book_key="calibre:9", run_id="run-9", calibre_book_id=9, status="applied")
        change = Change(
            book_key=book.book_key,
            run_id=book.run_id,
            before_metadata={"title": "Original"},
            after_metadata={"title": "Applied"},
            backup_opf_path="/missing/original.opf",
            status="applied",
        )
        session.add(book)
        session.add(change)
        session.commit()
        operation_id = queue_undo_operation(session, change)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()

        reconcile_incomplete_operations(session, MagicMock())

        session.refresh(operation)
        session.refresh(book)
        event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
        assert operation.state == "unknown"
        assert book.status == "error"
        assert event.status == "failed"
        assert session.get(BookWriteLock, book.book_key) is None


def _minimal_claim_setup(session: Session, suffix: str) -> str:
    operation_id = f"op-{suffix}"
    session.add(
        OperationLedger(
            operation_id=operation_id,
            idempotency_key=f"key-{suffix}",
            operation_type="metadata",
            book_key=f"calibre:{suffix}",
            state="requested",
        )
    )
    session.add(
        OutboxEvent(
            event_id=f"ev-{suffix}",
            aggregate_id=operation_id,
            event_type="operation.requested",
        )
    )
    session.add(BookRecord(book_key=f"calibre:{suffix}", run_id="run-1"))
    session.commit()
    return operation_id


def test_claim_lost_race_returns_none_on_integrity_error() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        _minimal_claim_setup(session, "race")
        with patch.object(
            session,
            "commit",
            side_effect=IntegrityError("INSERT", {}, Exception("duplicate key")),
        ):
            assert claim_next_operation(session) is None


def test_claim_propagates_non_integrity_db_failures() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        _minimal_claim_setup(session, "boom")
        with (
            patch.object(session, "commit", side_effect=RuntimeError("db gone")),
            pytest.raises(RuntimeError, match="db gone"),
        ):
            claim_next_operation(session)
