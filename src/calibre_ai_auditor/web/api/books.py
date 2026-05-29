import logging
from collections.abc import Generator
from typing import Any

from fastapi import APIRouter, Depends, HTTPException
from sqlmodel import Session, select

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage
from calibre_ai_auditor.vectors.client import VectorClient
from calibre_ai_auditor.vectors.embeddings import get_embedding_client
from calibre_ai_auditor.vectors.search import VectorSearcher
from calibre_ai_auditor.web.schemas import APIResponse

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/books", tags=["Books"])


def get_session() -> Generator[Session, None, None]:
    settings = load_settings()
    engine = get_engine(settings)
    with Session(engine) as session:
        yield session


def get_settings() -> Settings:
    return load_settings()


@router.get("", response_model=APIResponse)
async def list_books(session: Session = Depends(get_session)) -> Any:
    statement = select(BookRecord).limit(100)
    books = session.exec(statement).all()
    return {"status": "success", "data": [b.model_dump() for b in books]}


@router.get("/all/duplicates", response_model=APIResponse)
async def get_duplicates(
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> Any:
    # 1. Fetch all books from database
    statement = select(BookRecord)
    books = session.exec(statement).all()

    # 2. Get direct duplicates (fuzzy matching and ISBN matching)
    from calibre_ai_auditor.rules.duplicates import find_duplicates

    duplicates = find_duplicates(books)

    # 3. If vectors are enabled, add semantic duplicates
    if settings.vectors.enabled:
        vclient = VectorClient(
            settings.vectors.qdrant_url,
            settings.vectors.collection,
            settings.vectors.enabled,
        )
        eclient = get_embedding_client(settings)
        searcher = VectorSearcher(vclient, eclient)

        processed_pairs = set()
        for book in books:
            title = book.current_metadata.get("title", "")
            if not title:
                continue

            try:
                similar_books = await searcher.find_similar(title)
                for sim in similar_books:
                    sim_key = sim.get("book_key")
                    if not sim_key or sim_key == book.book_key:
                        continue

                    # Sort keys to prevent duplicate pairs (e.g. A-B and B-A)
                    pair = tuple(sorted([book.book_key, sim_key]))
                    if pair not in processed_pairs:
                        processed_pairs.add(pair)
                        score = sim.get("score", 0.0)
                        if score > 0.85:
                            duplicates.append(
                                {
                                    "type": "semantic_similarity",
                                    "books": [book.book_key, sim_key],
                                    "value": f"Similarity score: {score:.2f}",
                                }
                            )
            except Exception as e:
                logger.error(f"Failed to query semantic duplicates for {book.book_key}: {e}")

    return {"status": "success", "data": duplicates}


@router.get("/{book_key}", response_model=APIResponse)
async def get_book(book_key: str, session: Session = Depends(get_session)) -> Any:
    statement = select(BookRecord).where(BookRecord.book_key == book_key)
    book = session.exec(statement).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book not found")
    return {"status": "success", "data": book.model_dump()}


@router.get("/{book_key}/evidence", response_model=APIResponse)
async def get_evidence(book_key: str, session: Session = Depends(get_session)) -> Any:
    statement = select(EvidencePackage).where(EvidencePackage.book_key == book_key)
    package = session.exec(statement).first()
    if not package:
        raise HTTPException(status_code=404, detail="Evidence package not found")
    return {"status": "success", "data": package.model_dump()}


@router.get("/{book_key}/resolution", response_model=APIResponse)
async def get_resolution(book_key: str, session: Session = Depends(get_session)) -> Any:
    statement = select(EvidencePackage).where(EvidencePackage.book_key == book_key)
    package = session.exec(statement).first()
    if not package or not package.decision:
        raise HTTPException(status_code=404, detail="Resolution not found")
    return {"status": "success", "data": package.decision}


@router.get("/{book_key}/similar", response_model=APIResponse)
async def get_similar_books(
    book_key: str,
    settings: Settings = Depends(get_settings),
    session: Session = Depends(get_session),
) -> Any:
    if not settings.vectors.enabled:
        raise HTTPException(status_code=400, detail="Vector search is disabled")

    statement = select(BookRecord).where(BookRecord.book_key == book_key)
    book = session.exec(statement).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book not found")

    title = book.current_metadata.get("title", "")
    if not title:
        return {"status": "success", "data": []}

    vclient = VectorClient(
        settings.vectors.qdrant_url,
        settings.vectors.collection,
        settings.vectors.enabled,
    )
    eclient = get_embedding_client(settings)
    searcher = VectorSearcher(vclient, eclient)

    results = await searcher.find_similar(title)
    return {"status": "success", "data": results}



