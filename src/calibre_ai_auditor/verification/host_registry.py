"""Multi-host LLM/vision/OCR host discovery and capability routing.

Discovers available homelab inference hosts (Ollama, LM Studio, vLLM, anything
OpenAI-compatible) and tracks per-host GPU capability, model inventory, and
current load.  Used by the ContentVerificationEngine's LLM witness and the
OCRRouter for GPU-aware provider selection.

Hosts can be:
  - Discovered via config (`settings.hosts`)
  - Pinned via static config (Felix's homelab: 3090 on .89, 5060 Ti + 1660 SUPER on .122)
  - Health-checked periodically with exponential backoff on failure

Felix's specific setup (from CLAUDE.md):
  - 192.168.0.89:1234  — Gaming PC RTX 3090, LM Studio
  - 192.168.0.122:11434 — Unraid Ollama (primary), RTX 5060 Ti + GTX 1660 SUPER
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

import httpx

logger = logging.getLogger(__name__)


class HostKind(StrEnum):
    ollama = "ollama"
    lmstudio = "lmstudio"
    openai_compat = "openai_compat"
    vllm = "vllm"


class GPUClass(StrEnum):
    """Coarse GPU capability bucket for routing decisions."""

    high = "high"     # 3090, 4090, A100, etc. — can run 13B+ models
    medium = "medium"  # 3060, 4060 Ti, 5060 Ti — 7B models comfortably
    low = "low"        # 1660 SUPER, etc. — 7B-q4 or smaller
    cpu = "cpu"


@dataclass
class InferenceHost:
    """A single inference host with its capabilities."""

    name: str                       # "felix-server", "gaming-pc"
    base_url: str                   # e.g. http://192.168.0.122:11434/v1
    kind: HostKind
    gpu_class: GPUClass
    gpu_name: str | None = None     # "RTX 3090", "RTX 5060 Ti"
    models: list[str] = field(default_factory=list)
    enabled: bool = True
    last_health_at: float = 0.0
    last_health_ok: bool = False
    notes: str = ""

    def supports_vision(self) -> bool:
        vision_keywords = ("vl", "vision", "minicpm-v", "llava", "pixtral", "gemma3")
        return any(any(k in m.lower() for k in vision_keywords) for m in self.models)


@dataclass
class HostRegistryConfig:
    """Static config for known hosts."""

    hosts: list[InferenceHost] = field(default_factory=list)
    health_check_interval_seconds: int = 300
    health_check_timeout_seconds: float = 5.0


def default_felix_homelab() -> list[InferenceHost]:
    """The default hosts for Felix's homelab.

    Per CLAUDE.md:
      - Gaming PC RTX 3090 (192.168.0.89:1234) — LM Studio, heavy work
      - Unraid Ollama (192.168.0.122:11434) — RTX 5060 Ti + 1660 SUPER, bulk work
    """
    return [
        InferenceHost(
            name="gaming-pc-3090",
            base_url="http://192.168.0.89:1234/v1",
            kind=HostKind.lmstudio,
            gpu_class=GPUClass.high,
            gpu_name="RTX 3090",
            notes="LM Studio on Felix's gaming PC. Best for heavy vision + 13B+ models.",
        ),
        InferenceHost(
            name="unraid-ollama",
            base_url="http://192.168.0.122:11434/v1",
            kind=HostKind.ollama,
            gpu_class=GPUClass.medium,
            gpu_name="RTX 5060 Ti + GTX 1660 SUPER",
            notes="Ollama on Unraid. Primary bulk OCR + embedding host.",
        ),
        InferenceHost(
            name="local-ollama",
            base_url="http://localhost:11434/v1",
            kind=HostKind.ollama,
            gpu_class=GPUClass.cpu,
            notes="Local Ollama fallback. CPU-only.",
        ),
    ]


class HostRegistry:
    """Tracks all known inference hosts and their current health."""

    def __init__(self, config: HostRegistryConfig | None = None):
        self.config = config or HostRegistryConfig()
        if not self.config.hosts:
            self.config.hosts = default_felix_homelab()
        self._client: httpx.AsyncClient | None = None

    async def __aenter__(self) -> HostRegistry:
        self._client = httpx.AsyncClient(
            timeout=httpx.Timeout(self.config.health_check_timeout_seconds)
        )
        return self

    async def __aexit__(self, *_exc: Any) -> None:
        if self._client:
            await self._client.aclose()
            self._client = None

    async def health_check_all(self) -> dict[str, bool]:
        """Probe every host's /v1/models endpoint. Updates last_health_* state."""
        if not self._client:
            self._client = httpx.AsyncClient(
                timeout=httpx.Timeout(self.config.health_check_timeout_seconds)
            )
        results: dict[str, bool] = {}
        for host in self.config.hosts:
            if not host.enabled:
                results[host.name] = False
                continue
            try:
                url = host.base_url.rstrip("/") + "/models"
                resp = await self._client.get(url)
                ok = resp.status_code == 200
                if ok:
                    data = resp.json()
                    if isinstance(data, dict) and "data" in data:
                        host.models = [m.get("id", "") for m in data["data"] if m.get("id")]
                    elif isinstance(data, list):
                        host.models = [m.get("name", m.get("id", "")) for m in data if isinstance(m, dict)]
                host.last_health_ok = ok
                import time as _t
                host.last_health_at = _t.time()
                results[host.name] = ok
            except Exception as e:
                logger.debug("Health check failed for %s: %s", host.name, e)
                host.last_health_ok = False
                results[host.name] = False
        return results

    def healthy_hosts(self) -> list[InferenceHost]:
        return [h for h in self.config.hosts if h.enabled and h.last_health_ok]

    def hosts_with_vision(self) -> list[InferenceHost]:
        return [h for h in self.healthy_hosts() if h.supports_vision()]

    def best_host_for(self, task: str) -> InferenceHost | None:
        """Pick the best host for a given task type.

        Tasks: "heavy_vision", "bulk_ocr", "embedding", "fast_utility"
        """
        healthy = self.healthy_hosts()
        if not healthy:
            return None
        if task == "heavy_vision":
            # Prefer high-GPU hosts (3090)
            for h in healthy:
                if h.gpu_class == GPUClass.high:
                    return h
            # Fall through to medium if no high
        if task in ("bulk_ocr", "embedding", "fast_utility"):
            for h in healthy:
                if h.gpu_class == GPUClass.medium:
                    return h
        # Last resort: first healthy
        return healthy[0]

    def summary(self) -> dict[str, Any]:
        return {
            "total_hosts": len(self.config.hosts),
            "healthy_hosts": len(self.healthy_hosts()),
            "hosts": [
                {
                    "name": h.name,
                    "base_url": h.base_url,
                    "gpu_class": h.gpu_class.value,
                    "gpu_name": h.gpu_name,
                    "models": h.models,
                    "last_health_ok": h.last_health_ok,
                    "supports_vision": h.supports_vision(),
                }
                for h in self.config.hosts
            ],
        }


async def discover_hosts(timeout: float = 5.0) -> list[InferenceHost]:
    """Probe the standard Felix homelab ports and return what's reachable."""
    candidates = [
        ("gaming-pc-3090", "http://192.168.0.89:1234/v1", HostKind.lmstudio, GPUClass.high, "RTX 3090"),
        ("unraid-ollama", "http://192.168.0.122:11434/v1", HostKind.ollama, GPUClass.medium, "RTX 5060 Ti + 1660 SUPER"),
        ("local-ollama", "http://localhost:11434/v1", HostKind.ollama, GPUClass.cpu, None),
    ]
    found: list[InferenceHost] = []
    async with httpx.AsyncClient(timeout=timeout) as client:
        for name, url, kind, gpu_class, gpu_name in candidates:
            try:
                resp = await client.get(url.rstrip("/") + "/models")
                if resp.status_code == 200:
                    data = resp.json()
                    models: list[str] = []
                    if isinstance(data, dict) and "data" in data:
                        models = [m.get("id", "") for m in data["data"] if m.get("id")]
                    elif isinstance(data, list):
                        models = [m.get("name", "") for m in data if isinstance(m, dict)]
                    found.append(
                        InferenceHost(
                            name=name, base_url=url, kind=kind,
                            gpu_class=gpu_class, gpu_name=gpu_name,
                            models=models, last_health_ok=True,
                        )
                    )
            except Exception:
                continue
    return found