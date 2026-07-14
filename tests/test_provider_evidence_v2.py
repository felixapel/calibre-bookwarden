from __future__ import annotations

import json
from pathlib import Path

import httpx
import pytest

from calibre_ai_auditor.extractors.multiformat import ExtractedSnippet, FormatInspection
from calibre_ai_auditor.providers.evidence_v2 import (
    EvidenceEnrichment,
    GoogleBooksEvidenceProvider,
    MinimalEvidenceLLMEnricher,
    OutboundRequestError,
    SafeHttpClient,
    build_remote_evidence_bundle,
    llm_observation_to_evidence,
    validate_connected_peer,
    validate_outbound_url,
)
from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    FormatEvidence,
    FormatEvidenceStatus,
)
from calibre_ai_auditor.verification.pipeline_v2 import BookSnapshot

ISBN = "9780306406157"
SHA = "a" * 64


def _public_resolver(_host: str) -> list[str]:
    return ["93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"]


def test_outbound_url_validation_rejects_non_https_unlisted_and_private_targets() -> None:
    with pytest.raises(OutboundRequestError, match="HTTPS"):
        validate_outbound_url(
            "http://www.googleapis.com/books/v1/volumes",
            allowed_hosts={"www.googleapis.com"},
            resolver=_public_resolver,
        )
    with pytest.raises(OutboundRequestError, match="allowlist"):
        validate_outbound_url(
            "https://metadata.internal/books",
            allowed_hosts={"www.googleapis.com"},
            resolver=_public_resolver,
        )
    with pytest.raises(OutboundRequestError, match="public"):
        validate_outbound_url(
            "https://www.googleapis.com/books/v1/volumes",
            allowed_hosts={"www.googleapis.com"},
            resolver=lambda _host: ["127.0.0.1"],
        )


def test_connected_peer_validation_closes_private_dns_rebinding_window() -> None:
    class Stream:
        def __init__(self, address: str) -> None:
            self.address = address

        def get_extra_info(self, _name: str) -> tuple[str, int]:
            return (self.address, 443)

    validate_connected_peer(httpx.Response(200, extensions={"network_stream": Stream("93.184.216.34")}))
    with pytest.raises(OutboundRequestError, match="non-public"):
        validate_connected_peer(httpx.Response(200, extensions={"network_stream": Stream("127.0.0.1")}))
    with pytest.raises(OutboundRequestError, match="peer address"):
        validate_connected_peer(httpx.Response(200))


@pytest.mark.asyncio
async def test_safe_http_client_rejects_redirects_and_oversized_json() -> None:
    redirect_transport = httpx.MockTransport(
        lambda _request: httpx.Response(302, headers={"location": "https://example.net/elsewhere"})
    )
    redirect_client = SafeHttpClient(
        allowed_hosts={"www.googleapis.com"},
        transport=redirect_transport,
        resolver=_public_resolver,
    )
    with pytest.raises(OutboundRequestError, match="redirect"):
        await redirect_client.get_json("https://www.googleapis.com/books/v1/volumes")

    huge = json.dumps({"value": "x" * 500}).encode()
    huge_transport = httpx.MockTransport(lambda _request: httpx.Response(200, content=huge))
    huge_client = SafeHttpClient(
        allowed_hosts={"www.googleapis.com"},
        transport=huge_transport,
        resolver=_public_resolver,
        max_response_bytes=100,
    )
    with pytest.raises(OutboundRequestError, match="size"):
        await huge_client.get_json("https://www.googleapis.com/books/v1/volumes")


@pytest.mark.asyncio
async def test_google_provider_only_emits_exact_checksum_valid_isbn_records() -> None:
    payload = {
        "items": [
            {
                "id": "exact",
                "selfLink": "https://www.googleapis.com/books/v1/volumes/exact",
                "volumeInfo": {
                    "title": "The Exact Book",
                    "authors": ["Ada Author"],
                    "publisher": "Correct Press",
                    "publishedDate": "2024-05-06",
                    "language": "eng",
                    "industryIdentifiers": [{"type": "ISBN_13", "identifier": ISBN}],
                },
            },
            {
                "id": "wrong",
                "volumeInfo": {
                    "title": "Different Edition",
                    "authors": ["Ada Author"],
                    "language": "eng",
                    "industryIdentifiers": [{"type": "ISBN_13", "identifier": "9783161484100"}],
                },
            },
        ]
    }
    transport = httpx.MockTransport(lambda _request: httpx.Response(200, json=payload))
    client = SafeHttpClient(
        allowed_hosts={"www.googleapis.com"},
        transport=transport,
        resolver=_public_resolver,
    )
    provider = GoogleBooksEvidenceProvider(client)

    evidence = await provider.fetch_by_isbn(ISBN)

    assert {item.root_id for item in evidence} == {"google_books:exact"}
    assert {item.field for item in evidence} == {
        "authors",
        "identifiers",
        "languages",
        "publisher",
        "pubdate",
        "title",
    }
    assert all(item.authoritative for item in evidence)
    assert all(item.independence_root == "google_books" for item in evidence)


def _inspection_with_long_snippets(tmp_path: Path) -> FormatInspection:
    return FormatInspection(
        format_evidence=FormatEvidence(
            path=str(tmp_path / "book.epub"),
            format="EPUB",
            sha256=SHA,
            status=FormatEvidenceStatus.readable,
            identifiers={"isbn": ISBN},
            title="The Exact Book",
            authors=["Ada Author"],
            languages=["eng"],
        ),
        snippets=[
            ExtractedSnippet(source="title_page", text="TITLE EVIDENCE " * 20, locator="title.xhtml"),
            ExtractedSnippet(source="copyright_page", text="COPYRIGHT EVIDENCE " * 20, locator="copy.xhtml"),
            ExtractedSnippet(source="body", text="PRIVATE BODY " * 100, locator="body.xhtml"),
        ],
    )


def test_remote_bundle_has_one_total_text_budget_and_excludes_body(tmp_path: Path) -> None:
    bundle, receipt = build_remote_evidence_bundle(
        book_key="calibre:1",
        inspections=[_inspection_with_long_snippets(tmp_path)],
        allow_remote_text=True,
        max_chars=80,
    )

    assert sum(len(item.text) for item in bundle.snippets) <= 80
    assert all(item.source in {"title_page", "copyright_page"} for item in bundle.snippets)
    assert "PRIVATE BODY" not in "".join(item.text for item in bundle.snippets)
    assert receipt.text_chars == sum(len(item.text) for item in bundle.snippets)
    assert receipt.image_count == 0
    assert receipt.payload_sha256


def test_llm_observation_is_non_authoritative_and_requires_literal_quote(tmp_path: Path) -> None:
    bundle, _receipt = build_remote_evidence_bundle(
        book_key="calibre:1",
        inspections=[_inspection_with_long_snippets(tmp_path)],
        allow_remote_text=True,
        max_chars=200,
    )
    rejected = llm_observation_to_evidence(
        bundle=bundle,
        response={"field": "title", "value": "Invented", "evidence_quote": "not in payload"},
        response_sha256="b" * 64,
    )
    accepted = llm_observation_to_evidence(
        bundle=bundle,
        response={"field": "title", "value": "The Exact Book", "evidence_quote": "TITLE EVIDENCE"},
        response_sha256="b" * 64,
    )

    assert rejected == []
    assert len(accepted) == 1
    assert accepted[0].source_kind is EvidenceSourceKind.llm
    assert accepted[0].authoritative is False


class _Provider:
    name = "remote-test"
    is_local = False


class _Response:
    content = {
        "observations": [
            {
                "field": "title",
                "value": "The Exact Book",
                "evidence_quote": "TITLE EVIDENCE",
            }
        ]
    }


class _Router:
    def __init__(self) -> None:
        self.calls = 0

    def get_provider_for_task(self, _task: str) -> _Provider:
        return _Provider()

    async def execute_structured(self, _task: str, _request: object, _schema: dict[str, object]) -> _Response:
        self.calls += 1
        return _Response()


@pytest.mark.asyncio
async def test_remote_llm_requires_config_and_per_run_consent(tmp_path: Path) -> None:
    router = _Router()
    enricher = MinimalEvidenceLLMEnricher(
        router=router,
        model="test-model",
        allow_remote_text=True,
        run_allows_remote_text=False,
        max_chars=80,
    )

    result = await enricher.collect(
        BookSnapshot(
            book_key="calibre:1",
            calibre_book_id=1,
            current_metadata={},
            files=[],
            snapshot_sha256=SHA,
        ),
        [_inspection_with_long_snippets(tmp_path)],
    )

    assert isinstance(result, EvidenceEnrichment)
    assert result.evidence == []
    assert router.calls == 0
    assert result.warnings == ["remote_llm_text_not_authorized"]


@pytest.mark.asyncio
async def test_llm_observations_are_cited_nonauthoritative_and_receipted(tmp_path: Path) -> None:
    router = _Router()
    enricher = MinimalEvidenceLLMEnricher(
        router=router,
        model="test-model",
        allow_remote_text=True,
        run_allows_remote_text=True,
        max_chars=80,
    )

    result = await enricher.collect(
        BookSnapshot(
            book_key="calibre:1",
            calibre_book_id=1,
            current_metadata={},
            files=[],
            snapshot_sha256=SHA,
        ),
        [_inspection_with_long_snippets(tmp_path)],
    )

    assert router.calls == 1
    assert len(result.evidence) == 1
    assert result.evidence[0].source_kind is EvidenceSourceKind.llm
    assert result.evidence[0].authoritative is False
    assert result.privacy_receipts[0]["provider"] == "remote-test"
    assert result.privacy_receipts[0]["remote"] is True
    assert result.privacy_receipts[0]["text_chars"] <= 80
