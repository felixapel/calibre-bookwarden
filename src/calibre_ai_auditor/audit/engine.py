import logging
from typing import Any

from sqlmodel import Session, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.evidence.builder import build_evidence_package
from calibre_ai_auditor.judge.engine import MetadataJudge
from calibre_ai_auditor.rules.engine import apply_confidence_thresholds, check_risks
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage

logger = logging.getLogger(__name__)


async def run_audit(
    settings: Settings,
    run_id: str,
    judge: bool = True,
    save_evidence: bool = True,
    progress_callback: Any | None = None,
) -> None:
    engine = get_engine(settings)
    judge_engine = MetadataJudge(settings)

    with Session(engine) as session:
        book_stmt = select(BookRecord).where(BookRecord.run_id == run_id)
        books = session.exec(book_stmt).all()

        total = len(books)
        for i, book in enumerate(books):
            logger.info(f"Auditing book {i+1}/{total}: {book.current_metadata.get('title')}")
            if progress_callback:
                progress_callback(i + 1, total)

            try:
                package = await build_evidence_package(book, settings)

                if judge:
                    verdict = await judge_engine.judge(package)

                    patch = verdict.get("proposed_patch", {})
                    risks = check_risks(book.current_metadata, patch)
                    verdict["risk_flags"] = list(set(verdict.get("risk_flags", []) + risks))

                    final_action = apply_confidence_thresholds(verdict)
                    verdict["recommended_action"] = final_action

                    package.decision = verdict
                    book.status = final_action
                else:
                    book.status = "audited"

                if save_evidence:
                    # Clean up old evidence for this book in this run if any
                    old_ev = session.exec(
                        select(EvidencePackage)
                        .where(EvidencePackage.book_key == book.book_key)
                        .where(EvidencePackage.run_id == run_id)
                    ).first()
                    if old_ev:
                        session.delete(old_ev)
                    session.add(package)

                session.add(book)
                session.commit()
            except Exception as e:
                logger.error(f"Failed to audit book {book.book_key}: {e}")
                continue
