import asyncio
import logging
from pathlib import Path
from typing import Any

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.evidence.locks import evidence_from_field_locks
from calibre_ai_auditor.evidence.models import EvidenceItem, EvidenceSourceType
from calibre_ai_auditor.evidence.priority import get_priority
from calibre_ai_auditor.evidence.resolver import build_resolution
from calibre_ai_auditor.extractors.cover_hashes import calculate_phash
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.ocr.engine import OCREngine
from calibre_ai_auditor.providers.registry import ProviderRegistry
from calibre_ai_auditor.storage.models import (
    BookRecord,
    Candidate,
    EvidencePackage,
    Snippet,
)

logger = logging.getLogger(__name__)


async def build_evidence_package(book: BookRecord, settings: Any) -> EvidencePackage:
    evidence_id = f"ev_{book.run_id}_{book.book_key.replace(':', '_')}"
    cli = CalibreCLI(settings.library.path)

    # 1. Collect Evidence Items
    evidence_items: list[EvidenceItem] = list(evidence_from_field_locks(book))

    # 1.1 Extract snippets and cover
    snippets: list[Snippet] = []
    cover_info = None
    comic_meta = None

    if book.files:
        file_path = Path(book.files[0]["path"])
        if file_path.exists():
            snippets = extract_snippets(file_path)

            # 1.1.1 Trigger OCR fallback if text is too short and it's a PDF
            total_text = "".join([s.text for s in snippets])
            if len(total_text) < 100 and file_path.suffix.lower() == ".pdf":
                ocr_engine = OCREngine()
                ocr_text = await ocr_engine.run_ocr(file_path)
                if ocr_text:
                    snippets.append(Snippet(source="ocr", text=ocr_text, page_range="1-3"))

            # 1.1.2 Extract cover
            cover_dir = Path(settings.storage.artifacts_dir) / "covers"
            cover_path = cover_dir / f"{book.book_key.replace(':', '_')}.jpg"
            cover_path.parent.mkdir(parents=True, exist_ok=True)
            try:
                cli.extract_cover(file_path, cover_path)
            except Exception as e:
                logger.warning(f"Failed to extract cover via CLI for {book.book_key}: {e}")

            # Fallback for PDFs if CLI failed or empty
            if (
                not cover_path.exists() or cover_path.stat().st_size == 0
            ) and file_path.suffix.lower() == ".pdf":
                try:
                    from calibre_ai_auditor.extractors.cover import extract_pdf_cover

                    extract_pdf_cover(file_path, cover_path)
                except Exception as e:
                    logger.warning(f"Failed PDF cover fallback extraction for {book.book_key}: {e}")

            # Fallback for ZIPs/CBZs if CLI failed or empty
            if (
                not cover_path.exists() or cover_path.stat().st_size == 0
            ) and file_path.suffix.lower() in (".cbz", ".cbr"):
                try:
                    from calibre_ai_auditor.extractors.cover import extract_zip_cover

                    extract_zip_cover(file_path, cover_path)
                except Exception as e:
                    logger.warning(f"Failed ZIP cover fallback extraction for {book.book_key}: {e}")

            # In Comics/Manga mode, extract metadata from ComicInfo.xml
            if file_path.suffix.lower() in (".cbz", ".cbr"):
                try:
                    from calibre_ai_auditor.extractors.comics import extract_comic_info_xml
                    comic_meta = extract_comic_info_xml(file_path)
                    if comic_meta:
                        for field, val in comic_meta.items():
                            if val:
                                evidence_items.append(
                                    EvidenceItem(
                                        id=f"{evidence_id}_comic_{field}",
                                        book_key=book.book_key,
                                        field=field,
                                        value=val,
                                        source_type=EvidenceSourceType.book_content,
                                        source_name="comicinfo_xml",
                                        priority=get_priority(EvidenceSourceType.book_content),
                                        confidence=95,
                                    )
                                )
                except Exception as e:
                    logger.warning(f"Failed to extract ComicInfo.xml for {book.book_key}: {e}")

            if cover_path.exists() and cover_path.stat().st_size > 0:
                try:
                    cover_info = {
                        "embedded_cover_path": str(cover_path),
                        "phash": calculate_phash(cover_path),
                        "vision_verification": None,
                    }
                    from calibre_ai_auditor.ocr.vision import VisionVerifier

                    verifier = VisionVerifier(settings)
                    vision_result = await verifier.verify_cover(cover_path)
                    if vision_result:
                        cover_info["vision_verification"] = vision_result

                        # Extract and add evidence items from cover vision
                        ext_title = vision_result.get("title")
                        if ext_title:
                            evidence_items.append(
                                EvidenceItem(
                                    id=f"{evidence_id}_v_title",
                                    book_key=book.book_key,
                                    field="title",
                                    value=ext_title,
                                    source_type=EvidenceSourceType.book_content,
                                    source_name="vision_verifier",
                                    priority=get_priority(EvidenceSourceType.book_content),
                                    confidence=int(vision_result.get("confidence", 0.8) * 100),
                                )
                            )
                        ext_authors = vision_result.get("authors")
                        if ext_authors:
                            evidence_items.append(
                                EvidenceItem(
                                    id=f"{evidence_id}_v_authors",
                                    book_key=book.book_key,
                                    field="authors",
                                    value=ext_authors,
                                    source_type=EvidenceSourceType.book_content,
                                    source_name="vision_verifier",
                                    priority=get_priority(EvidenceSourceType.book_content),
                                    confidence=int(vision_result.get("confidence", 0.8) * 100),
                                )
                            )
                        ext_isbn = vision_result.get("isbn")
                        if ext_isbn:
                            evidence_items.append(
                                EvidenceItem(
                                    id=f"{evidence_id}_v_isbn",
                                    book_key=book.book_key,
                                    field="isbn",
                                    value=ext_isbn,
                                    source_type=EvidenceSourceType.book_content,
                                    source_name="vision_verifier",
                                    priority=get_priority(EvidenceSourceType.book_content),
                                    confidence=int(vision_result.get("confidence", 0.8) * 100),
                                )
                            )
                except Exception as e:
                    logger.warning(
                        f"Failed to verify cover via vision model for {book.book_key}: {e}"
                    )

    # 2. Heuristic extraction
    extracted = extract_heuristics([s.model_dump() for s in snippets])
    if comic_meta:
        extracted.update(comic_meta)

    # Create EvidenceItems from heuristics
    if extracted.get("title"):
        evidence_items.append(
            EvidenceItem(
                id=f"{evidence_id}_h_title",
                book_key=book.book_key,
                field="title",
                value=extracted["title"],
                source_type=EvidenceSourceType.book_content,
                source_name="heuristics",
                priority=get_priority(EvidenceSourceType.book_content),
                confidence=80,
            )
        )

    isbn = extracted.get("identifiers", {}).get("isbn")
    if isbn:
        evidence_items.append(
            EvidenceItem(
                id=f"{evidence_id}_h_isbn",
                book_key=book.book_key,
                field="isbn",
                value=isbn,
                source_type=EvidenceSourceType.book_content,
                source_name="heuristics",
                priority=get_priority(EvidenceSourceType.book_content),
                confidence=95,
            )
        )

    # 3. Fetch candidates from providers
    title = book.current_metadata.get("title") or extracted.get("title")
    authors = book.current_metadata.get("authors") or extracted.get("authors", [])
    current_isbn = book.current_metadata.get("identifiers", {}).get("isbn")
    search_isbn = current_isbn or isbn

    registry = ProviderRegistry(cli)
    providers = registry.get_providers()
    tasks = [p.fetch_candidates(title=title, authors=authors, isbn=search_isbn) for p in providers]
    results = await asyncio.gather(*tasks)

    candidates: list[Candidate] = []
    for res in results:
        candidates.extend(res)

    # Convert candidates to evidence items
    for cand in candidates:
        if cand.metadata.title:
            evidence_items.append(
                EvidenceItem(
                    id=f"{evidence_id}_p_{cand.candidate_id}_title",
                    book_key=book.book_key,
                    field="title",
                    value=cand.metadata.title,
                    source_type=EvidenceSourceType.external_provider,
                    source_name=cand.provider,
                    priority=get_priority(EvidenceSourceType.external_provider),
                    confidence=90,
                    provider_url=cand.provider_url,
                )
            )

    # 4. Resolve Deterministically
    resolution = build_resolution(book.book_key, evidence_items)

    # 5. Build the final EvidencePackage
    package = EvidencePackage(
        evidence_id=evidence_id,
        book_key=book.book_key,
        run_id=book.run_id,
        current=book.current_metadata,
        extracted=extracted,
        candidates=[c.model_dump() for c in candidates],
        snippets=[s.model_dump() for s in snippets],
        cover=cover_info,
        risk_flags=resolution.risk_flags,
    )

    # Store the deterministic decision
    package.decision = resolution.model_dump()

    return package
