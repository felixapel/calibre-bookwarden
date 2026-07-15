import uuid
from pathlib import Path
from typing import Annotated, Any

from fastapi import APIRouter, Depends, File, HTTPException, UploadFile

from calibre_ai_auditor.comics.pipeline import enrich_comic_observations
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.storage.models import EvidencePackage
from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)
from calibre_ai_auditor.web.schemas import APIResponse, InspectRequest

router = APIRouter()

MAX_UPLOAD_BYTES = 100 * 1024 * 1024
UPLOAD_SUFFIXES = {".epub", ".pdf", ".mobi", ".azw3", ".cbz", ".cbr"}


def get_settings() -> Settings:
    return load_settings()


def check_sandbox(path: Path, settings: Settings) -> Path:
    # Resolve the path to get the absolute normalized path
    resolved_path = path.resolve()

    # Allowed roots
    allowed_roots: list[Path] = []
    if settings.library.path:
        allowed_roots.append(Path(settings.library.path).resolve())

    # Check if resolved_path is relative to any of the allowed roots
    is_safe = False
    for root in allowed_roots:
        try:
            if resolved_path == root or resolved_path.is_relative_to(root):
                is_safe = True
                break
        except ValueError:
            continue

    if not is_safe:
        raise HTTPException(status_code=403, detail="Access denied: Path is outside sandboxed directories")

    return resolved_path


@router.get("/inspect/fs", response_model=APIResponse)
async def list_fs(
    dir_path: str = "/library",
    settings: Settings = Depends(get_settings),
) -> dict[str, Any]:
    target = Path(dir_path)
    try:
        target = check_sandbox(target, settings)
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e)) from None  # noqa: B904 from None

    if not target.exists() or not target.is_dir():
        raise HTTPException(status_code=404, detail="Directory not found")

    directories = []
    files = []
    try:
        for entry in target.iterdir():
            if entry.is_dir():
                directories.append({"name": entry.name, "path": str(entry)})
            elif entry.suffix.lower() in [".epub", ".pdf", ".cbz", ".cbr"]:
                try:
                    size = entry.stat().st_size
                except Exception:
                    size = 0
                files.append({"name": entry.name, "path": str(entry), "size_bytes": size})
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e)) from e  # noqa: B904

    resolved_library_path = Path(settings.library.path).resolve() if settings.library.path else None
    parent_dir = str(target.parent) if (resolved_library_path and target != resolved_library_path) else None

    return {
        "status": "success",
        "data": {
            "current_dir": str(target),
            "parent_dir": parent_dir,
            "directories": sorted(directories, key=lambda x: x["name"].lower()),
            "files": sorted(files, key=lambda x: x["name"].lower()),
        },
    }


async def _build_inspection_package(path: Path, settings: Settings, *, no_providers: bool) -> dict[str, Any]:
    """Build an inspection result for a single file using the v1.0 engine.

    For legacy /inspect compatibility we still return an EvidencePackage-shaped
    dict, but the verdict comes from the v1.0 ContentVerificationEngine.
    """
    snippets = extract_snippets(path)
    extracted = extract_heuristics([s.model_dump() for s in snippets])
    snippet_text = "\n".join(s.text for s in snippets)

    book_key = f"path:{path}"

    # Use pipeline to replace scattered comic merge + extract_cbz_cover_and_vision (handles ComicInfo + vision + komf)
    raw = {
        "title": extracted.get("title"),
        "authors": extracted.get("authors") or [],
        "isbn": (extracted.get("identifiers") or {}).get("isbn"),
        "series": extracted.get("series"),
        "series_index": extracted.get("series_index"),
        "volume": extracted.get("volume"),
        "chapter": extracted.get("chapter"),
        "series_position": extracted.get("series_position"),
        "publisher": extracted.get("publisher"),
        "published_date": extracted.get("published_date"),
    }
    is_cbz = path.suffix.lower() in (".cbz", ".cbr")
    enriched_decl, enriched_obs = await enrich_comic_observations(settings, path if is_cbz else None, None, raw, raw)
    # apply back to extracted for legacy extra_extracted and declared/obs below
    for k, v in enriched_obs.items():
        if v is not None:
            extracted[k] = v

    # Build declared metadata from the (now pipeline-enriched) extracted
    declared = DeclaredMetadata(
        title=enriched_decl.get("title") or extracted.get("title"),
        authors=enriched_decl.get("authors") or extracted.get("authors") or [],
        isbn=enriched_decl.get("isbn") or (extracted.get("identifiers") or {}).get("isbn"),
        series=enriched_decl.get("series") or extracted.get("series"),
        series_index=enriched_decl.get("series_index") or extracted.get("series_index"),
        volume=enriched_decl.get("volume"),
        chapter=enriched_decl.get("chapter"),
        series_position=enriched_decl.get("series_position"),
        publisher=enriched_decl.get("publisher") or extracted.get("publisher"),
        published_date=enriched_decl.get("published_date") or extracted.get("published_date"),
    )
    observed = ObservationSet(
        title_page_text=snippet_text[:5000] if snippet_text else None,
        body_sample=snippet_text[:10000] if snippet_text else None,
        title_extracted=enriched_obs.get("title") or extracted.get("title"),
        authors_extracted=enriched_obs.get("authors") or extracted.get("authors") or [],
        isbn_extracted=enriched_obs.get("isbn") or (extracted.get("identifiers") or {}).get("isbn"),
        series_extracted=enriched_obs.get("series") or extracted.get("series"),
        series_index_extracted=enriched_obs.get("series_index") or extracted.get("series_index"),
        volume_extracted=enriched_obs.get("volume"),
        chapter_extracted=enriched_obs.get("chapter"),
        series_position_extracted=enriched_obs.get("series_position"),
        cover_vision=enriched_obs.get("cover_vision"),
        publisher_extracted=enriched_obs.get("publisher") or extracted.get("publisher"),
        date_extracted=enriched_obs.get("published_date") or extracted.get("published_date"),
        evidence_quality="high" if len(snippet_text) > 500 else "low",
    )

    engine = ContentVerificationEngine()
    verdict = engine.verify(
        book_key=book_key,
        run_id="inspect",
        declared=declared,
        observed=observed,
    )

    # Return as EvidencePackage-shaped dict for back-compat with existing UI
    # C3: include merged comic fields in extracted (current uses enhanced declared)
    extra_extracted: dict[str, Any] = {}
    for comic_key in (
        "series",
        "series_index",
        "volume",
        "chapter",
        "series_position",
        "publisher",
        "published_date",
        "tags",
    ):
        if comic_key in extracted and extracted[comic_key] is not None:
            extra_extracted[comic_key] = extracted[comic_key]
    package = EvidencePackage(
        evidence_id=f"ev_inspect_path_{path.name}",
        book_key=book_key,
        run_id="inspect",
        current=declared.model_dump() if hasattr(declared, "model_dump") else {},
        extracted={
            "title": extracted.get("title"),
            "authors": extracted.get("authors", []),
            "identifiers": extracted.get("identifiers", {}),
            **extra_extracted,
        },
        candidates=[],
        snippets=[s.model_dump() for s in snippets],
        risk_flags=verdict.risk_flags,
        decision=verdict.model_dump(),
    )
    return package.model_dump()


@router.post("/inspect/path", response_model=APIResponse)
async def inspect_path(req: InspectRequest, settings: Annotated[Settings, Depends(get_settings)]) -> dict[str, Any]:
    path = Path(req.path)
    try:
        path = check_sandbox(path, settings)
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e)) from e  # noqa: B904

    if not path.exists():
        raise HTTPException(status_code=404, detail=f"File not found: {path}")

    package = await _build_inspection_package(path, settings, no_providers=req.no_providers)
    return {"status": "success", "data": package}


@router.post("/inspect/upload", response_model=APIResponse)
async def inspect_upload(
    settings: Annotated[Settings, Depends(get_settings)],
    file: UploadFile = File(...),
    no_providers: bool = False,
) -> dict[str, Any]:
    if not settings.allow_remote_file_upload:
        raise HTTPException(
            status_code=403,
            detail="Remote file upload is disabled (set BOOKAUDIT_ALLOW_REMOTE_FILE_UPLOAD=true)",
        )

    suffix = Path(file.filename or "").suffix.lower()
    if suffix not in UPLOAD_SUFFIXES:
        raise HTTPException(status_code=400, detail=f"Unsupported file type: {suffix}")

    upload_dir = Path(settings.storage.artifacts_dir) / "uploads"
    upload_dir.mkdir(parents=True, exist_ok=True)
    dest = upload_dir / f"{uuid.uuid4().hex}{suffix}"

    size = 0
    try:
        with dest.open("wb") as out:
            while chunk := await file.read(1024 * 1024):
                size += len(chunk)
                if size > MAX_UPLOAD_BYTES:
                    raise HTTPException(status_code=413, detail="Upload exceeds 100MB limit")
                out.write(chunk)
    except HTTPException:
        dest.unlink(missing_ok=True)
        raise
    except Exception as exc:
        dest.unlink(missing_ok=True)
        raise HTTPException(status_code=500, detail=f"Failed to save upload: {exc}") from exc

    try:
        package = await _build_inspection_package(dest, settings, no_providers=no_providers)
        return {"status": "success", "data": package}
    finally:
        dest.unlink(missing_ok=True)
