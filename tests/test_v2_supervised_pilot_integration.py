"""Required real-service round trip for the supervised Manifestation V2 pilot."""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
from collections.abc import Generator
from datetime import UTC, datetime
from pathlib import Path
from uuid import uuid4
from zipfile import ZIP_DEFLATED, ZIP_STORED, ZipFile

import pytest
from fastapi.testclient import TestClient
from PIL import Image, ImageDraw, ImageFont
from sqlalchemy import create_engine, text
from sqlalchemy.engine import Engine, make_url
from sqlmodel import Session, select

from calibre_ai_auditor.apply.coordinator import PilotGuard
from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.apply.heartbeat import (
    WRITER_HEARTBEAT_KEY,
    library_root_sha256,
    publish_writer_heartbeat,
)
from calibre_ai_auditor.apply.writer import MetadataWriter, claim_next_operation
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.extractors.multiformat import inspect_format
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import (
    BookRecord,
    Change,
    EvidencePackage,
    OutboxEvent,
)
from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    IdentityTier,
    SourceEvidence,
    resolve_manifestation,
)
from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, BookSnapshot, EvidencePackageV2
from calibre_ai_auditor.web.api import apply as apply_api
from calibre_ai_auditor.web.api import review_v2 as review_api
from calibre_ai_auditor.web.app import app

_REQUIRED = bool(
    os.environ.get("TEST_POSTGRES_DSN")
    and os.environ.get("TEST_VALKEY_URL")
    and shutil.which("calibredb")
    and shutil.which("tesseract")
)


def test_required_live_epub_uses_the_production_internal_identity_extractor(tmp_path: Path) -> None:
    epub = tmp_path / "exact-pilot.epub"
    _write_minimal_epub(epub)

    inspection = inspect_format(epub, library_root=tmp_path)

    assert inspection.format_evidence.status.value == "readable"
    assert inspection.format_evidence.identifiers == {"isbn": "9780306406157"}
    assert inspection.format_evidence.title == "EXACT PILOT TITLE"
    assert inspection.format_evidence.authors == ["Exact Pilot Author"]
    assert inspection.format_evidence.languages == ["eng"]
    assert any(
        item.source_kind is EvidenceSourceKind.content_native
        and item.field == "identifiers"
        and item.value == {"isbn": "9780306406157"}
        for item in inspection.evidence
    )


@pytest.mark.v2_live
@pytest.mark.ocr_live
@pytest.mark.skipif(
    not _REQUIRED,
    reason="TEST_POSTGRES_DSN, TEST_VALKEY_URL, calibredb, and tesseract are required",
)
def test_api_to_writer_readback_and_undo_with_real_services(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    dsn = os.environ["TEST_POSTGRES_DSN"]
    valkey_url = os.environ["TEST_VALKEY_URL"]
    if "test" not in (make_url(dsn).database or "").lower():
        pytest.fail("TEST_POSTGRES_DSN must name an unmistakably disposable test database")
    monkeypatch.delenv("PYTHONPATH", raising=False)

    engine = create_engine(dsn)
    _truncate_runtime_tables(engine)
    library = (tmp_path / "library").absolute()
    artifacts = (tmp_path / "artifacts").absolute()
    library.mkdir()
    artifacts.mkdir()
    epub = tmp_path / "original.epub"
    _write_minimal_epub(epub)
    subprocess.run(
        ["calibredb", "add", str(epub), "--with-library", str(library)],
        check=True,
        capture_output=True,
        text=True,
    )

    ocr_text, ocr_artifact_sha256 = _run_real_ocr(tmp_path)
    assert "EXACT PILOT TITLE" in " ".join(ocr_text.upper().split())

    cli = CalibreCLI(library)
    calibre_book = cli.list_books()[0]
    book_id = int(calibre_book["id"])
    subprocess.run(
        [
            "calibredb",
            "set_metadata",
            str(book_id),
            "--field",
            "title:Incorrect Library Title",
            "--with-library",
            str(library),
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    before = cli.show_metadata(book_id)
    assert before["title"] == "Incorrect Library Title"
    assert before.get("identifiers", {}).get("isbn") == "9780306406157"
    format_path = Path(str(before["formats"][0])).absolute()
    assert format_path.is_file()
    book_key = f"calibre:{book_id}"
    suffix = uuid4().hex
    run_id = f"v2-live-{suffix}"
    evidence_id = f"evidence-{suffix}"
    exact_title = "EXACT PILOT TITLE"
    exact_authors = ["Exact Pilot Author"]
    exact_languages = ["eng"]
    format_sha256 = hashlib.sha256(format_path.read_bytes()).hexdigest()
    inspection = inspect_format(format_path, library_root=library)
    assert inspection.format_evidence.sha256 == format_sha256
    assert inspection.format_evidence.title == exact_title
    assert inspection.format_evidence.authors == exact_authors
    assert inspection.format_evidence.languages == exact_languages
    assert any(
        item.source_kind is EvidenceSourceKind.content_native
        and item.field == "identifiers"
        and item.manifestation_ids.get("isbn") == "9780306406157"
        for item in inspection.evidence
    )

    provider_root = f"structured-catalog-fixture-{suffix}"
    provider_payload = (
        b'{"isbn":"9780306406157","title":"EXACT PILOT TITLE","authors":["Exact Pilot Author"],"languages":["eng"]}'
    )
    provider_artifact_sha256 = hashlib.sha256(provider_payload).hexdigest()
    provider_evidence = [
        SourceEvidence(
            evidence_id=f"provider-{field}-{suffix}",
            root_id=provider_root,
            source_kind=EvidenceSourceKind.provider_structured,
            field=field,
            value=value,
            manifestation_ids={"isbn": "9780306406157"},
            locator="deterministic structured-catalog integration fixture",
            artifact_sha256=provider_artifact_sha256,
            source_url="https://catalog.fixture.invalid/isbn/9780306406157",
            authoritative=True,
        )
        for field, value in (
            ("identifiers", {"isbn": "9780306406157"}),
            ("title", exact_title),
            ("authors", exact_authors),
            ("languages", exact_languages),
        )
    ]
    ocr_evidence = SourceEvidence(
        evidence_id=f"ocr-observation-{suffix}",
        root_id=f"ocr-title-page-{suffix}",
        source_kind=EvidenceSourceKind.ocr_observation,
        field="title",
        value=ocr_text.strip(),
        manifestation_ids={"isbn": "9780306406157"},
        locator="real Tesseract title-page OCR",
        artifact_sha256=ocr_artifact_sha256,
        authoritative=False,
    )
    source_evidence = [*inspection.evidence, *provider_evidence, ocr_evidence]
    formats = [inspection.format_evidence]
    identity = resolve_manifestation(
        formats=formats,
        evidence=source_evidence,
        current_metadata=before,
    )
    assert identity.tier is IdentityTier.tier_a
    assert identity.auto_patch == {"title": exact_title}

    snapshot = BookSnapshot(
        book_key=book_key,
        calibre_book_id=book_id,
        current_metadata=before,
        files=[str(format_path)],
        library_root=str(library),
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    package = EvidencePackageV2(
        evidence_id=evidence_id,
        run_id=run_id,
        book_key=book_key,
        created_at=datetime.now(UTC),
        state=BookAuditState.shadowed,
        snapshot=snapshot,
        formats=formats,
        source_evidence=source_evidence,
        identity=identity,
    ).seal()

    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key=book_key,
                run_id=run_id,
                calibre_book_id=book_id,
                source="calibre",
                files=[{"path": str(format_path), "format": "EPUB"}],
                current_metadata=before,
                status="shadowed",
            )
        )
        session.add(
            EvidencePackage(
                evidence_id=evidence_id,
                book_key=book_key,
                run_id=run_id,
                schema_version=2,
                current=package.snapshot.current_metadata,
                extracted=package.identity.model_dump(mode="json"),
                observations=package.model_dump(mode="json"),
            )
        )
        session.commit()

    api_key = "v2-live-api-key-at-least-thirty-two-characters"
    release_digest = f"sha256:{'e' * 64}"
    settings = Settings(
        library={"path": str(library), "read_only": True},
        storage={"sqlite_path": tmp_path / "unused.db", "artifacts_dir": artifacts},
        database={"backend": "postgres", "postgres_dsn": dsn},
        queue={"backend": "valkey", "valkey_url": valkey_url},
        api_key=api_key,
        manifestation_v2={
            "supervised_pilot": {
                "enabled": True,
                "pilot_id": f"pilot-{suffix}",
                "release_digest": release_digest,
                "max_operations": 5,
            }
        },
    )
    publish_writer_heartbeat(
        valkey_url,
        f"writer-{suffix}",
        release_digest=release_digest,
        alembic_revision=expected_schema_revision(),
        library_root_sha256=library_root_sha256(library),
        pilot_id=f"pilot-{suffix}",
        max_operations=5,
    )
    monkeypatch.setenv("BOOKAUDIT_API_KEY", api_key)

    def override_session() -> Generator[Session, None, None]:
        with Session(engine) as session:
            yield session

    app.dependency_overrides[apply_api.get_session] = override_session
    app.dependency_overrides[apply_api.get_settings] = lambda: settings
    app.dependency_overrides[review_api.get_session] = override_session
    client = TestClient(app)
    headers = {"X-API-Key": api_key}

    try:
        authorization = client.post(
            f"/api/review/v2/{evidence_id}/authorize",
            json={
                "reason": (
                    "Production EPUB extraction, exact ISBN, structured catalog fixture, and Tesseract witness matched"
                )
            },
            headers=headers,
        )
        assert authorization.status_code == 200, authorization.text
        authorization_id = authorization.json()["data"]["authorization_id"]

        queued = client.post(
            "/api/apply/v2",
            json={
                "force": True,
                "evidence_id": evidence_id,
                "authorization_id": authorization_id,
            },
            headers=headers,
        )
        assert queued.status_code == 200, queued.text
        operation_id = queued.json()["data"]["operation_id"]

        with Session(engine) as session:
            outbox = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
            assert outbox.status == "pending"
            assert claim_next_operation(session, lease_owner=f"writer-{suffix}") == operation_id
            applied = MetadataWriter(
                cli,
                ApplyEngine(cli, artifacts),
                pilot=PilotGuard(
                    enabled=True,
                    pilot_id=f"pilot-{suffix}",
                    library_root=str(library),
                    release_digest=release_digest,
                    alembic_revision=expected_schema_revision(),
                    max_operations=5,
                    writer_ready=True,
                ),
            ).process(session, operation_id)
            assert applied.state == "succeeded", applied.error
            assert applied.change_id is not None
            change_id = applied.change_id

        assert cli.show_metadata(book_id)["title"] == exact_title
        readback = client.get(f"/api/operations/{operation_id}", headers=headers)
        assert readback.status_code == 200
        assert readback.json()["data"]["state"] == "succeeded"

        undo = client.post(f"/api/undo/{change_id}", json={"force": True}, headers=headers)
        assert undo.status_code == 200, undo.text
        undo_operation_id = undo.json()["data"]["operation_id"]
        with Session(engine) as session:
            assert claim_next_operation(session, lease_owner=f"writer-{suffix}") == undo_operation_id
            undone = MetadataWriter(cli, ApplyEngine(cli, artifacts)).process(session, undo_operation_id)
            assert undone.state == "succeeded"
            change = session.get(Change, change_id)
            assert change is not None
            assert change.status == "undone"

        assert cli.show_metadata(book_id)["title"] == before["title"]
        undo_readback = client.get(f"/api/operations/{undo_operation_id}", headers=headers)
        assert undo_readback.status_code == 200
        assert undo_readback.json()["data"]["state"] == "succeeded"
    finally:
        app.dependency_overrides.pop(apply_api.get_session, None)
        app.dependency_overrides.pop(apply_api.get_settings, None)
        app.dependency_overrides.pop(review_api.get_session, None)
        import redis

        valkey = redis.from_url(valkey_url)
        try:
            valkey.delete(WRITER_HEARTBEAT_KEY)
        finally:
            valkey.close()
        _truncate_runtime_tables(engine)
        engine.dispose()


def _truncate_runtime_tables(engine: Engine) -> None:
    with engine.begin() as connection:
        connection.execute(
            text(
                'TRUNCATE TABLE operationincidentacknowledgement, bookwritelock, "change", '
                "outboxevent, operationledger, "
                "pilotsession, manualauthorization, evidencepackage, verificationresult, "
                "verificationrun, bookrecord, run, covervisioncache RESTART IDENTITY CASCADE"
            )
        )


def _run_real_ocr(tmp_path: Path) -> tuple[str, str]:
    image_path = tmp_path / "title-page.png"
    image = Image.new("RGB", (1600, 320), "white")
    font_path = Path("/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf")
    font = ImageFont.truetype(str(font_path), 72) if font_path.is_file() else ImageFont.load_default()
    ImageDraw.Draw(image).text((40, 90), "EXACT PILOT TITLE", fill="black", font=font)
    image.save(image_path)
    result = subprocess.run(
        ["tesseract", str(image_path), "stdout", "--psm", "6", "-l", "eng"],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout, hashlib.sha256(image_path.read_bytes()).hexdigest()


def _write_minimal_epub(path: Path) -> None:
    container = """<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>"""
    opf = """<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>EXACT PILOT TITLE</dc:title><dc:creator>Exact Pilot Author</dc:creator>
    <dc:identifier id="bookid">urn:isbn:9780306406157</dc:identifier><dc:language>eng</dc:language>
  </metadata>
  <manifest><item id="title" href="title.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="title"/></spine>
</package>"""
    title_page = """<html xmlns="http://www.w3.org/1999/xhtml"><head><title>EXACT PILOT TITLE</title></head>
<body><h1>EXACT PILOT TITLE</h1><p>Exact Pilot Author</p>
<p>Copyright 2026 Exact Pilot Press. ISBN 9780306406157</p></body></html>"""
    with ZipFile(path, "w") as archive:
        archive.writestr("mimetype", "application/epub+zip", compress_type=ZIP_STORED)
        archive.writestr("META-INF/container.xml", container, compress_type=ZIP_DEFLATED)
        archive.writestr("OEBPS/content.opf", opf, compress_type=ZIP_DEFLATED)
        archive.writestr("OEBPS/title.xhtml", title_page, compress_type=ZIP_DEFLATED)
