"""Validated external evidence adapters for the manifestation V2 pipeline."""

from __future__ import annotations

import asyncio
import hashlib
import ipaddress
import json
import logging
import socket
from collections.abc import Callable
from typing import TYPE_CHECKING, Any, cast
from urllib.parse import urlsplit

import httpx
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from calibre_ai_auditor.extractors.multiformat import FormatInspection
from calibre_ai_auditor.llm.schemas import LLMRequest
from calibre_ai_auditor.verification.identity_v2 import (
    ALLOWED_PATCH_FIELDS,
    EvidenceSourceKind,
    SourceEvidence,
    validate_isbn,
)
from calibre_ai_auditor.verification.pipeline_v2 import EvidenceEnrichment

if TYPE_CHECKING:
    from calibre_ai_auditor.config.settings import Settings
    from calibre_ai_auditor.verification.pipeline_v2 import BookSnapshot

logger = logging.getLogger(__name__)

Resolver = Callable[[str], list[str]]


class OutboundRequestError(RuntimeError):
    pass


def _system_resolver(host: str) -> list[str]:
    return sorted({str(item[4][0]) for item in socket.getaddrinfo(host, 443, type=socket.SOCK_STREAM)})


def validate_outbound_url(
    raw_url: str,
    *,
    allowed_hosts: set[str] | frozenset[str],
    resolver: Resolver = _system_resolver,
) -> str:
    """Validate a fixed provider URL before any network request is attempted."""
    parsed = urlsplit(raw_url)
    if parsed.scheme != "https":
        raise OutboundRequestError("provider requests require HTTPS")
    if parsed.username or parsed.password:
        raise OutboundRequestError("provider URL credentials are forbidden")
    if parsed.port not in (None, 443):
        raise OutboundRequestError("provider URL uses a forbidden port")
    host = (parsed.hostname or "").rstrip(".").lower()
    normalized_allowlist = {item.rstrip(".").lower() for item in allowed_hosts}
    if host not in normalized_allowlist:
        raise OutboundRequestError("provider host is not in the allowlist")
    try:
        addresses = resolver(host)
    except OSError as exc:
        raise OutboundRequestError("provider DNS resolution failed") from exc
    if not addresses:
        raise OutboundRequestError("provider DNS returned no addresses")
    for address in addresses:
        try:
            parsed_ip = ipaddress.ip_address(address.split("%", 1)[0])
        except ValueError as exc:
            raise OutboundRequestError("provider DNS returned an invalid address") from exc
        if not parsed_ip.is_global:
            raise OutboundRequestError("provider DNS must resolve only to public addresses")
    return raw_url


def validate_connected_peer(response: httpx.Response) -> None:
    """Fail closed if the socket connected to a non-public address after DNS validation."""
    stream = response.extensions.get("network_stream")
    get_extra_info = getattr(stream, "get_extra_info", None)
    peer = get_extra_info("server_addr") if callable(get_extra_info) else None
    if not isinstance(peer, (tuple, list)) or not peer:
        raise OutboundRequestError("provider connection did not expose its peer address")
    try:
        address = ipaddress.ip_address(str(peer[0]).split("%", 1)[0])
    except ValueError as exc:
        raise OutboundRequestError("provider connection exposed an invalid peer address") from exc
    if not address.is_global:
        raise OutboundRequestError("provider connection reached a non-public peer address")


class SafeHttpClient:
    def __init__(
        self,
        *,
        allowed_hosts: set[str],
        resolver: Resolver = _system_resolver,
        transport: httpx.AsyncBaseTransport | None = None,
        max_response_bytes: int = 2 * 1024 * 1024,
        timeout_seconds: float = 15.0,
    ) -> None:
        self.allowed_hosts = frozenset(allowed_hosts)
        self.resolver = resolver
        self.transport = transport
        self.max_response_bytes = max_response_bytes
        self.timeout_seconds = timeout_seconds

    async def get_json(self, url: str, *, params: dict[str, Any] | None = None) -> dict[str, Any]:
        safe_url = validate_outbound_url(url, allowed_hosts=self.allowed_hosts, resolver=self.resolver)
        timeout = httpx.Timeout(self.timeout_seconds)
        async with (
            httpx.AsyncClient(
                timeout=timeout,
                follow_redirects=False,
                trust_env=False,
                transport=self.transport,
            ) as client,
            client.stream("GET", safe_url, params=params) as response,
        ):
            if self.transport is None:
                validate_connected_peer(response)
            if 300 <= response.status_code < 400:
                raise OutboundRequestError("provider redirect was rejected")
            if response.status_code < 200 or response.status_code >= 300:
                raise OutboundRequestError(f"provider returned HTTP {response.status_code}")
            content_type = response.headers.get("content-type", "").split(";", 1)[0].strip().lower()
            if content_type and content_type not in {"application/json", "text/json"}:
                raise OutboundRequestError("provider returned a non-JSON content type")
            chunks: list[bytes] = []
            total = 0
            async for chunk in response.aiter_bytes():
                total += len(chunk)
                if total > self.max_response_bytes:
                    raise OutboundRequestError("provider response exceeded the size limit")
                chunks.append(chunk)
        try:
            parsed = json.loads(b"".join(chunks))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise OutboundRequestError("provider returned invalid JSON") from exc
        if not isinstance(parsed, dict):
            raise OutboundRequestError("provider response root must be an object")
        return parsed


class GoogleIndustryIdentifier(BaseModel):
    type: str = ""
    identifier: str = ""


class GoogleVolumeInfo(BaseModel):
    title: str | None = None
    authors: list[str] = Field(default_factory=list)
    publisher: str | None = None
    published_date: str | None = Field(default=None, alias="publishedDate")
    language: str | None = None
    industry_identifiers: list[GoogleIndustryIdentifier] = Field(
        default_factory=list,
        alias="industryIdentifiers",
    )
    image_links: dict[str, str] = Field(default_factory=dict, alias="imageLinks")


class GoogleVolume(BaseModel):
    id: str
    self_link: str | None = Field(default=None, alias="selfLink")
    volume_info: GoogleVolumeInfo = Field(default_factory=GoogleVolumeInfo, alias="volumeInfo")


class GoogleResponse(BaseModel):
    items: list[GoogleVolume] = Field(default_factory=list)


def _record_sha(value: object) -> str:
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, default=str).encode()
    return hashlib.sha256(encoded).hexdigest()


def _record_evidence(
    *,
    provider: str,
    record_id: str,
    source_url: str | None,
    response_sha256: str,
    identifiers: dict[str, str],
    values: dict[str, Any],
) -> list[SourceEvidence]:
    root_id = f"{provider}:{record_id}"
    result: list[SourceEvidence] = []
    for field, value in values.items():
        if field not in ALLOWED_PATCH_FIELDS or value in (None, [], {}):
            continue
        evidence_hash = hashlib.sha256(f"{root_id}\0{field}".encode()).hexdigest()[:20]
        result.append(
            SourceEvidence(
                evidence_id=f"ev_{evidence_hash}",
                root_id=root_id,
                independence_root=provider,
                source_kind=EvidenceSourceKind.provider_structured,
                field=field,
                value=value,
                manifestation_ids=identifiers,
                source_url=source_url,
                artifact_sha256=response_sha256,
                authoritative=True,
            )
        )
    return result


class GoogleBooksEvidenceProvider:
    endpoint = "https://www.googleapis.com/books/v1/volumes"

    def __init__(self, client: SafeHttpClient) -> None:
        self.client = client

    async def fetch_by_isbn(self, isbn: str) -> list[SourceEvidence]:
        canonical = validate_isbn(isbn)
        if canonical is None:
            return []
        raw = await self.client.get_json(self.endpoint, params={"q": f"isbn:{canonical}", "maxResults": 10})
        try:
            response = GoogleResponse.model_validate(raw)
        except ValidationError as exc:
            raise OutboundRequestError("Google Books response failed schema validation") from exc
        evidence: list[SourceEvidence] = []
        for item in response.items[:10]:
            record_ids = {
                valid
                for identifier in item.volume_info.industry_identifiers
                if (valid := validate_isbn(identifier.identifier))
            }
            if canonical not in record_ids:
                continue
            identifiers = {"isbn": canonical}
            values: dict[str, Any] = {
                "identifiers": identifiers,
                "title": item.volume_info.title,
                "authors": item.volume_info.authors,
                "languages": [item.volume_info.language.lower()] if item.volume_info.language else [],
                "publisher": item.volume_info.publisher,
                "pubdate": item.volume_info.published_date,
            }
            if cover := item.volume_info.image_links.get("thumbnail"):
                values["cover"] = {"source_url": cover, "manifestation_isbn": canonical}
            evidence.extend(
                _record_evidence(
                    provider="google_books",
                    record_id=item.id,
                    source_url=item.self_link,
                    response_sha256=_record_sha(item.model_dump(mode="json")),
                    identifiers=identifiers,
                    values=values,
                )
            )
        return evidence


class OpenLibraryEvidenceProvider:
    endpoint = "https://openlibrary.org/api/books"

    def __init__(self, client: SafeHttpClient) -> None:
        self.client = client

    async def fetch_by_isbn(self, isbn: str) -> list[SourceEvidence]:
        canonical = validate_isbn(isbn)
        if canonical is None:
            return []
        key = f"ISBN:{canonical}"
        raw = await self.client.get_json(
            self.endpoint,
            params={"bibkeys": key, "format": "json", "jscmd": "data"},
        )
        record = raw.get(key)
        if not isinstance(record, dict):
            return []
        authors = [
            item.get("name") for item in record.get("authors", []) if isinstance(item, dict) and item.get("name")
        ]
        publishers = [
            item.get("name") for item in record.get("publishers", []) if isinstance(item, dict) and item.get("name")
        ]
        languages = []
        for item in record.get("languages", []):
            if isinstance(item, dict) and isinstance(item.get("key"), str):
                languages.append(item["key"].rsplit("/", 1)[-1].lower())
        identifiers = {"isbn": canonical}
        values: dict[str, Any] = {
            "identifiers": identifiers,
            "title": record.get("title"),
            "authors": authors,
            "languages": languages,
            "publisher": publishers[0] if publishers else None,
            "pubdate": record.get("publish_date"),
        }
        cover = record.get("cover")
        if isinstance(cover, dict) and isinstance(cover.get("large"), str):
            values["cover"] = {"source_url": cover["large"], "manifestation_isbn": canonical}
        return _record_evidence(
            provider="openlibrary",
            record_id=canonical,
            source_url=record.get("url") if isinstance(record.get("url"), str) else None,
            response_sha256=_record_sha(record),
            identifiers=identifiers,
            values=values,
        )


class StructuredEvidenceEnricher:
    def __init__(self, providers: list[Any]) -> None:
        self.providers = providers

    @classmethod
    def from_settings(cls, settings: Settings) -> StructuredEvidenceEnricher:
        providers: list[Any] = []
        if settings.providers.google_books:
            providers.append(GoogleBooksEvidenceProvider(SafeHttpClient(allowed_hosts={"www.googleapis.com"})))
        if settings.providers.openlibrary:
            providers.append(OpenLibraryEvidenceProvider(SafeHttpClient(allowed_hosts={"openlibrary.org"})))
        return cls(providers)

    async def collect(
        self,
        _book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> list[SourceEvidence]:
        isbn_values = {
            value
            for inspection in inspections
            for namespace, value in inspection.format_evidence.identifiers.items()
            if namespace == "isbn" and validate_isbn(value)
        }
        if len(isbn_values) != 1:
            return []
        isbn = next(iter(isbn_values))

        async def fetch(provider: Any) -> list[SourceEvidence]:
            try:
                return cast(list[SourceEvidence], await provider.fetch_by_isbn(isbn))
            except (OutboundRequestError, httpx.HTTPError, TimeoutError, ValueError) as exc:
                logger.warning("Structured provider %s failed closed: %s", type(provider).__name__, exc)
                return []

        batches = await asyncio.gather(*(fetch(provider) for provider in self.providers))
        return [item for batch in batches for item in batch]


class RemoteSnippet(BaseModel):
    model_config = ConfigDict(extra="forbid")

    source: str
    locator: str
    text: str


class RemoteEvidenceBundle(BaseModel):
    model_config = ConfigDict(extra="forbid")

    book_key: str
    manifestation_ids: dict[str, str] = Field(default_factory=dict)
    snippets: list[RemoteSnippet] = Field(default_factory=list)


class EgressReceipt(BaseModel):
    model_config = ConfigDict(extra="forbid")

    book_key: str
    text_chars: int
    image_count: int = 0
    payload_sha256: str
    provider: str | None = None
    remote: bool | None = None


def build_remote_evidence_bundle(
    *,
    book_key: str,
    inspections: list[FormatInspection],
    allow_remote_text: bool,
    max_chars: int,
) -> tuple[RemoteEvidenceBundle, EgressReceipt]:
    remaining = max(0, max_chars)
    snippets: list[RemoteSnippet] = []
    if allow_remote_text:
        for inspection in inspections:
            for item in inspection.snippets:
                if item.source not in {"title_page", "copyright_page"} or remaining <= 0:
                    continue
                selected = item.text[:remaining]
                if selected:
                    snippets.append(RemoteSnippet(source=item.source, locator=item.locator, text=selected))
                    remaining -= len(selected)
    ids = {
        (namespace, value)
        for inspection in inspections
        for namespace, value in inspection.format_evidence.identifiers.items()
    }
    manifestation_ids = dict(ids) if len(ids) == 1 else {}
    bundle = RemoteEvidenceBundle(book_key=book_key, manifestation_ids=manifestation_ids, snippets=snippets)
    payload = bundle.model_dump(mode="json")
    receipt = EgressReceipt(
        book_key=book_key,
        text_chars=sum(len(item.text) for item in snippets),
        payload_sha256=_record_sha(payload),
    )
    return bundle, receipt


class LLMObservation(BaseModel):
    model_config = ConfigDict(extra="forbid")

    field: str
    value: Any
    evidence_quote: str = Field(min_length=1, max_length=1000)


class LLMObservationBatch(BaseModel):
    model_config = ConfigDict(extra="forbid")

    observations: list[LLMObservation] = Field(default_factory=list, max_length=12)


def llm_observation_to_evidence(
    *,
    bundle: RemoteEvidenceBundle,
    response: dict[str, Any],
    response_sha256: str,
) -> list[SourceEvidence]:
    try:
        observation = LLMObservation.model_validate(response)
    except ValidationError:
        return []
    if observation.field not in ALLOWED_PATCH_FIELDS:
        return []
    matching = [item for item in bundle.snippets if observation.evidence_quote in item.text]
    if not matching:
        return []
    evidence_hash = hashlib.sha256(f"{bundle.book_key}\0{observation.field}\0{response_sha256}".encode()).hexdigest()[
        :20
    ]
    return [
        SourceEvidence(
            evidence_id=f"ev_{evidence_hash}",
            root_id=f"llm:{response_sha256[:20]}",
            independence_root="llm",
            source_kind=EvidenceSourceKind.llm,
            field=observation.field,
            value=observation.value,
            manifestation_ids=bundle.manifestation_ids,
            locator=matching[0].locator,
            artifact_sha256=response_sha256,
            authoritative=False,
        )
    ]


class MinimalEvidenceLLMEnricher:
    """Ask an LLM to transcribe bounded evidence; never treat it as authority."""

    def __init__(
        self,
        *,
        router: Any,
        model: str,
        allow_remote_text: bool,
        run_allows_remote_text: bool,
        max_chars: int,
    ) -> None:
        self.router = router
        self.model = model
        self.allow_remote_text = allow_remote_text
        self.run_allows_remote_text = run_allows_remote_text
        self.max_chars = max(0, max_chars)

    async def collect(
        self,
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment:
        try:
            provider = self.router.get_provider_for_task("deep_reasoning")
        except (RuntimeError, ValueError) as exc:
            logger.warning("LLM evidence enrichment unavailable: %s", exc)
            return EvidenceEnrichment(warnings=["llm_provider_unavailable"])

        remote = not bool(provider.is_local)
        if remote and not (self.allow_remote_text and self.run_allows_remote_text):
            return EvidenceEnrichment(warnings=["remote_llm_text_not_authorized"])

        bundle, receipt = build_remote_evidence_bundle(
            book_key=book.book_key,
            inspections=inspections,
            allow_remote_text=True,
            max_chars=self.max_chars,
        )
        receipt = receipt.model_copy(update={"provider": provider.name, "remote": remote})
        if not bundle.snippets:
            return EvidenceEnrichment(
                privacy_receipts=[receipt.model_dump(mode="json")],
                warnings=["llm_evidence_payload_empty"],
            )

        schema = LLMObservationBatch.model_json_schema()
        request = LLMRequest(
            model=self.model,
            temperature=0.0,
            json_schema=True,
            max_tokens=1200,
            messages=[
                {
                    "role": "system",
                    "content": (
                        "Transcribe bibliographic facts only from the supplied excerpts. "
                        "Every observation must include a short exact quote copied from an excerpt. "
                        "Do not infer an edition and do not use outside knowledge."
                    ),
                },
                {
                    "role": "user",
                    "content": json.dumps(bundle.model_dump(mode="json"), ensure_ascii=False),
                },
            ],
        )
        try:
            response = await self.router.execute_structured("deep_reasoning", request, schema)
            content = response.content
            if isinstance(content, str):
                content = json.loads(content)
            batch = LLMObservationBatch.model_validate(content)
        except (ValidationError, ValueError, TypeError, json.JSONDecodeError) as exc:
            logger.warning("LLM evidence response rejected: %s", exc)
            return EvidenceEnrichment(
                privacy_receipts=[receipt.model_dump(mode="json")],
                warnings=["llm_response_rejected"],
            )
        except Exception as exc:  # pragma: no cover - provider-specific failures
            logger.warning("LLM evidence request failed closed: %s", exc)
            return EvidenceEnrichment(
                privacy_receipts=[receipt.model_dump(mode="json")],
                warnings=["llm_request_failed"],
            )

        response_sha256 = _record_sha(content)
        evidence = [
            item
            for observation in batch.observations
            for item in llm_observation_to_evidence(
                bundle=bundle,
                response=observation.model_dump(mode="json"),
                response_sha256=response_sha256,
            )
        ]
        warnings = [] if len(evidence) == len(batch.observations) else ["uncited_llm_observation_rejected"]
        return EvidenceEnrichment(
            evidence=evidence,
            privacy_receipts=[receipt.model_dump(mode="json")],
            warnings=warnings,
        )


class CompositeEvidenceEnricher:
    """Combine enrichers while preserving evidence, receipts, and warnings."""

    def __init__(self, enrichers: list[Any]) -> None:
        self.enrichers = enrichers

    async def _collect_selected(
        self,
        enrichers: list[Any],
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment:
        combined = EvidenceEnrichment()
        for enricher in enrichers:
            result = await enricher.collect(book, inspections)
            normalized = result if isinstance(result, EvidenceEnrichment) else EvidenceEnrichment(evidence=result)
            combined.evidence.extend(normalized.evidence)
            combined.privacy_receipts.extend(normalized.privacy_receipts)
            combined.warnings.extend(normalized.warnings)
        return combined

    async def collect_materialized(
        self,
        book: BookSnapshot,
        inspection: FormatInspection,
    ) -> EvidenceEnrichment:
        """Run only enrichers that require the exported ebook to remain present."""
        selected = [item for item in self.enrichers if getattr(item, "requires_materialized_files", False)]
        return await self._collect_selected(selected, book, [inspection])

    async def collect_aggregate(
        self,
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment:
        """Run provider and text-only enrichment once over all logical formats."""
        selected = [item for item in self.enrichers if not getattr(item, "requires_materialized_files", False)]
        return await self._collect_selected(selected, book, inspections)

    async def collect(
        self,
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment:
        return await self._collect_selected(self.enrichers, book, inspections)
