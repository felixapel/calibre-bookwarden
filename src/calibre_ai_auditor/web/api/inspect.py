import uuid
from pathlib import Path
from typing import Annotated, Any

from fastapi import APIRouter, Depends, File, HTTPException, UploadFile

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.evidence.builder import build_evidence_package
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage
from calibre_ai_auditor.web.schemas import InspectRequest

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


@router.get("/inspect/fs")
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
        raise HTTPException(status_code=400, detail=str(e))

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
        raise HTTPException(status_code=500, detail=str(e))
    
    resolved_library_path = Path(settings.library.path).resolve() if settings.library.path else None
    parent_dir = str(target.parent) if (resolved_library_path and target != resolved_library_path) else None

    return {
        "current_dir": str(target),
        "parent_dir": parent_dir,
        "directories": sorted(directories, key=lambda x: x["name"].lower()),
        "files": sorted(files, key=lambda x: x["name"].lower())
    }


async def _build_inspection_package(
    path: Path, settings: Settings, *, no_providers: bool
) -> dict[str, Any]:
    book_record = BookRecord(
        book_key=f"path:{path}",
        run_id="inspect",
        source="direct_path",
        current_metadata={"title": None, "authors": []},
        files=[{"path": str(path), "format": path.suffix[1:].lower()}],
    )

    snippets = extract_snippets(path)
    extracted = extract_heuristics([s.model_dump() for s in snippets])

    if no_providers:
        package = EvidencePackage(
            evidence_id=f"ev_inspect_path_{path.name}",
            book_key=f"path:{path}",
            run_id="inspect",
            current=book_record.current_metadata,
            extracted=extracted,
            candidates=[],
            snippets=[s.model_dump() for s in snippets],
            risk_flags=[],
        )
    else:
        if not book_record.current_metadata.get("title"):
            book_record.current_metadata["title"] = extracted.get("title")
        if not book_record.current_metadata.get("authors"):
            book_record.current_metadata["authors"] = extracted.get("authors", [])

        package = await build_evidence_package(book_record, settings)

    return package.model_dump()


@router.post("/inspect/path")
async def inspect_path(
    req: InspectRequest, settings: Annotated[Settings, Depends(get_settings)]
) -> dict[str, Any]:
    path = Path(req.path)
    try:
        path = check_sandbox(path, settings)
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))

    if not path.exists():
        raise HTTPException(status_code=404, detail=f"File not found: {path}")

    return await _build_inspection_package(path, settings, no_providers=req.no_providers)


@router.post("/inspect/upload")
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

    return await _build_inspection_package(dest, settings, no_providers=no_providers)
