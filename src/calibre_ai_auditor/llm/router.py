import copy
import logging
from typing import Any

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.llm.base import LLMProvider
from calibre_ai_auditor.llm.providers.google_provider import GoogleProvider
from calibre_ai_auditor.llm.providers.lmstudio_provider import LMStudioProvider
from calibre_ai_auditor.llm.providers.ollama_provider import OllamaProvider
from calibre_ai_auditor.llm.providers.openai_provider import OpenAIProvider
from calibre_ai_auditor.llm.schemas import LLMRequest, LLMResponse

logger = logging.getLogger(__name__)


class LLMRouter:
    def __init__(self, settings: Settings):
        self.settings = settings
        self.providers: dict[str, LLMProvider] = {}

        # Initialize configured providers
        if settings.open_ai_api_key and settings.open_ai_api_key.strip():
            self.providers["openai"] = OpenAIProvider(
                api_key=settings.open_ai_api_key, base_url=settings.open_ai_base_url
            )

        if settings.ollama_enabled and settings.ollama_base_url:
            self.providers["ollama"] = OllamaProvider(base_url=settings.ollama_base_url)

        if settings.gemini_api_key and settings.gemini_api_key.strip():
            self.providers["google"] = GoogleProvider(api_key=settings.gemini_api_key)

        if settings.lmstudio_enabled and settings.lmstudio_base_url:
            self.providers["lmstudio"] = LMStudioProvider(base_url=settings.lmstudio_base_url)

    def get_provider_for_task(self, task: str = "deep_reasoning") -> LLMProvider:
        """
        Simple routing logic.
        """
        if task == "vision":
            model = self.settings.vision_model or ""
        else:
            model = self.settings.judge_model or ""

        # 1. Route to Google Gemini if model is gemini and google is configured
        if "gemini" in model.lower() and "google" in self.providers:
            return self.providers["google"]

        # 2. Route to LM Studio if model/config designates it
        if "lmstudio" in model.lower() and "lmstudio" in self.providers:
            return self.providers["lmstudio"]

        # 3. Route to OpenAI if configured
        if (
            "openai" in self.providers
            and (settings := self.settings)
            and settings.open_ai_api_key
            and settings.open_ai_api_key.strip()
        ):
            return self.providers["openai"]

        # 4. Fallback to Ollama if configured
        if "ollama" in self.providers:
            return self.providers["ollama"]

        # 5. Default fallback to whatever is registered
        if self.providers:
            return next(iter(self.providers.values()))

        raise ValueError("No LLM providers configured")

    def _apply_privacy_filters(self, provider: LLMProvider, request: LLMRequest) -> LLMRequest:
        request = copy.deepcopy(request)
        if not provider.is_local:
            if not self.settings.privacy.allow_remote_text:
                logger.warning(
                    f"Privacy alert: Running task with remote provider '{provider.name}' but "
                    "allow_remote_text is set to False. All metadata and snippet text will be "
                    "hidden from the LLM, which may prevent successful auditing."
                )
            # Enforce remote privacy limits by filtering messages directly
            filtered_messages = []
            for msg in request.messages:
                # If we had a complex message structure with images, we'd strip them here.
                # For now, if allow_remote_images is False, we just pass the text.
                if isinstance(msg.get("content"), list):
                    new_content = []
                    for item in msg["content"]:
                        is_image = item.get("type") == "image_url"
                        if is_image and not self.settings.privacy.allow_remote_images:
                            continue
                        is_text = item.get("type") == "text"
                        if is_text and not self.settings.privacy.allow_remote_text:
                            # Strip text if remote text is disallowed
                            item["text"] = "[HIDDEN FOR PRIVACY]"
                        new_content.append(item)
                    msg["content"] = new_content
                elif isinstance(msg.get("content"), str):
                    if not self.settings.privacy.allow_remote_text:
                        msg["content"] = "[HIDDEN FOR PRIVACY]"
                filtered_messages.append(msg)
            request.messages = filtered_messages

        return request

    async def execute_structured(
        self, task: str, request: LLMRequest, schema: dict[str, Any]
    ) -> LLMResponse:
        provider = self.get_provider_for_task(task)
        request = self._apply_privacy_filters(provider, request)
        logger.info(f"Routing task '{task}' to provider '{provider.name}'")
        return await provider.structured(request, schema)

    async def execute_chat(self, task: str, request: LLMRequest) -> LLMResponse:
        provider = self.get_provider_for_task(task)
        request = self._apply_privacy_filters(provider, request)
        logger.info(f"Routing chat task '{task}' to provider '{provider.name}'")
        return await provider.chat(request)
