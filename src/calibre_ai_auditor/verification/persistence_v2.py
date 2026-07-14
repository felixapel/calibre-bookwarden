"""Durable, resumable persistence for manifestation V2 verification runs."""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import uuid4

from sqlalchemy.engine import Engine
from sqlmodel import Session, select

from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, VerificationResult, VerificationRun
from calibre_ai_auditor.verification.pipeline_v2 import (
    TERMINAL_BOOK_STATES,
    BookAuditState,
    EvidencePackageV2,
)


class SQLAuditStore:
    """Persist state transitions and sealed evidence without enabling V1 apply."""

    def __init__(self, engine: Engine, run_id: str) -> None:
        self.engine = engine
        self.run_id = run_id

    def start(self, *, book_keys: list[str], mode: str, use_llm: bool) -> None:
        with Session(self.engine) as session:
            run = session.exec(select(VerificationRun).where(VerificationRun.run_id == self.run_id)).first()
            if run is None:
                run = VerificationRun(
                    run_id=self.run_id,
                    total=len(book_keys),
                    use_llm=use_llm,
                    pipeline_version="manifestation-v2",
                    mode=mode,
                )
                session.add(run)
            elif run.pipeline_version != "manifestation-v2" or run.mode != mode:
                raise ValueError("run_id already belongs to a different verification contract")
            existing = {
                item.book_key
                for item in session.exec(
                    select(VerificationResult).where(VerificationResult.run_id == self.run_id)
                ).all()
            }
            if existing and existing != set(book_keys):
                raise ValueError("run_id library membership differs from the frozen V2 snapshot")
            for book_key in book_keys:
                if book_key not in existing:
                    session.add(
                        VerificationResult(
                            result_id=str(uuid4()),
                            run_id=self.run_id,
                            book_key=book_key,
                            state=BookAuditState.pending.value,
                        )
                    )
            session.commit()

    def record_state(self, book_key: str, state: BookAuditState) -> None:
        with Session(self.engine) as session:
            result = self._result(session, book_key)
            result.state = state.value
            session.add(result)
            session.commit()

    def record_package(self, package: EvidencePackageV2) -> None:
        if package.run_id != self.run_id or not package.verify_seal():
            raise ValueError("evidence package identity or seal is invalid")
        payload = package.model_dump(mode="json")
        with Session(self.engine) as session:
            book = session.exec(select(BookRecord).where(BookRecord.book_key == package.book_key)).first()
            files = [
                {"path": path, "format": path.rsplit(".", 1)[-1].upper() if "." in path else "UNKNOWN"}
                for path in package.snapshot.files
            ]
            if book is None:
                book = BookRecord(
                    book_key=package.book_key,
                    run_id=package.run_id,
                    calibre_book_id=package.snapshot.calibre_book_id,
                    source="calibre",
                    files=files,
                    current_metadata=package.snapshot.current_metadata,
                    status=package.state.value,
                )
            else:
                book.run_id = package.run_id
                book.calibre_book_id = package.snapshot.calibre_book_id
                book.files = files
                book.current_metadata = package.snapshot.current_metadata
                book.status = package.state.value
            session.add(book)
            stored = session.exec(
                select(EvidencePackage).where(EvidencePackage.evidence_id == package.evidence_id)
            ).first()
            if stored is None:
                session.add(
                    EvidencePackage(
                        evidence_id=package.evidence_id,
                        book_key=package.book_key,
                        run_id=package.run_id,
                        schema_version=package.schema_version,
                        current=package.snapshot.current_metadata,
                        extracted=package.identity.model_dump(mode="json"),
                        risk_flags=package.identity.risk_flags,
                        # V1 apply only consumes BookVerdict in decision. V2 has a
                        # separate sealed contract and must never masquerade as V1.
                        decision=None,
                        observations=payload,
                    )
                )
            elif stored.observations != payload:
                raise ValueError("evidence_id already exists with a different sealed payload")
            result = self._result(session, package.book_key)
            result.evidence_id = package.evidence_id
            result.state = package.state.value
            result.verdict = {
                "schema_version": package.schema_version,
                "evidence_id": package.evidence_id,
                "book_key": package.book_key,
                "state": package.state.value,
                "identity": package.identity.model_dump(mode="json"),
                "warnings": package.warnings,
            }
            session.add(result)
            self._refresh_counts(session)
            session.commit()

    def finish(self, status: str) -> None:
        with Session(self.engine) as session:
            run = self._run(session)
            self._refresh_counts(session, run=run)
            run.status = status
            run.finished_at = datetime.now(UTC)
            session.add(run)
            session.commit()

    def resumable_book_keys(self) -> set[str]:
        terminal = {state.value for state in TERMINAL_BOOK_STATES if state is not BookAuditState.failed}
        with Session(self.engine) as session:
            rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == self.run_id)).all()
            resumable: set[str] = set()
            for row in rows:
                if not row.evidence_id or row.state not in terminal:
                    continue
                stored = session.exec(
                    select(EvidencePackage).where(EvidencePackage.evidence_id == row.evidence_id)
                ).first()
                if stored is None or stored.schema_version != 2 or not stored.observations:
                    continue
                try:
                    package = EvidencePackageV2.model_validate(stored.observations)
                except ValueError:
                    continue
                if (
                    package.verify_seal()
                    and package.evidence_id == row.evidence_id
                    and package.run_id == self.run_id
                    and package.book_key == row.book_key
                    and stored.book_key == package.book_key
                    and stored.run_id == package.run_id
                    and stored.current == package.snapshot.current_metadata
                    and stored.extracted == package.identity.model_dump(mode="json")
                ):
                    resumable.add(row.book_key)
            return resumable

    def _result(self, session: Session, book_key: str) -> VerificationResult:
        result = session.exec(
            select(VerificationResult)
            .where(VerificationResult.run_id == self.run_id)
            .where(VerificationResult.book_key == book_key)
        ).first()
        if result is None:
            raise ValueError(f"book {book_key} is not part of run {self.run_id}")
        return result

    def _run(self, session: Session) -> VerificationRun:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == self.run_id)).first()
        if run is None:
            raise ValueError(f"verification run {self.run_id} has not been started")
        return run

    def _refresh_counts(self, session: Session, *, run: VerificationRun | None = None) -> None:
        durable_run = run or self._run(session)
        rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == self.run_id)).all()
        counts: dict[str, int] = {}
        completed = 0
        terminal = {state.value for state in TERMINAL_BOOK_STATES}
        for row in rows:
            if row.state in terminal and row.evidence_id:
                completed += 1
                counts[row.state] = counts.get(row.state, 0) + 1
        durable_run.completed = completed
        durable_run.counts = counts
        session.add(durable_run)
