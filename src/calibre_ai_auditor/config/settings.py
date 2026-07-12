from pathlib import Path
from typing import Any

import yaml
from pydantic import AliasChoices, BaseModel, Field, SecretStr
from pydantic_settings import BaseSettings, PydanticBaseSettingsSource, SettingsConfigDict


class LibrarySettings(BaseModel):
    path: Path | None = Field(None, validation_alias=AliasChoices("BOOKAUDIT_LIBRARY_PATH", "library_path", "path"))
    read_only: bool = True


class StorageSettings(BaseModel):
    sqlite_path: Path = Field(
        Path(".state/bookaudit.db"),
        validation_alias=AliasChoices("BOOKAUDIT_DB_PATH", "sqlite_path", "path"),
    )
    artifacts_dir: Path = Field(
        Path(".artifacts"),
        validation_alias=AliasChoices("BOOKAUDIT_ARTIFACTS_DIR", "artifacts_dir"),
    )


class ProviderSettings(BaseModel):
    calibre_fetch: bool = True
    openlibrary: bool = True
    google_books: bool = True


class PrivacySettings(BaseModel):
    allow_remote_text: bool = False
    allow_remote_images: bool = False
    max_remote_chars: int = Field(
        4000,
        validation_alias=AliasChoices("max_remote_chars", "max_snippet_chars"),
    )
    max_remote_images: int = 1


def _default_library() -> LibrarySettings:
    return LibrarySettings()  # type: ignore


def _default_storage() -> StorageSettings:
    return StorageSettings()  # type: ignore


class TikaSettings(BaseModel):
    enabled: bool = False
    base_url: str = "http://tika:9998"
    timeout_seconds: int = 30
    write_limit: int = 200000
    max_embedded_resources: int = 5


class ExtractorSettings(BaseModel):
    tika: TikaSettings = Field(default_factory=TikaSettings)


class DatabaseSettings(BaseModel):
    backend: str = "sqlite"  # sqlite | postgres
    sqlite_path: Path = Path("/state/bookaudit.db")
    postgres_dsn: str | None = None


class QueueSettings(BaseModel):
    backend: str = "memory"
    valkey_url: str = "redis://valkey:6379/0"
    connect_timeout_seconds: float = 1.0


class RateLimitSettings(BaseModel):
    enabled: bool = True
    backend: str = "memory"
    valkey_url: str = "redis://valkey:6379/1"


class VectorSettings(BaseModel):
    enabled: bool = False
    qdrant_url: str = "http://qdrant:6333"
    collection: str = "bookaudit_evidence"
    embedding_provider: str = "ollama"
    embedding_model: str = "nomic-embed-text"
    local_only: bool = True


class MangaProviders(BaseModel):
    anilist: bool = False
    mal: bool = False
    mangaupdates: bool = False
    komf: bool = False


class MangaSettings(BaseModel):
    enabled: bool = False
    providers: MangaProviders = Field(default_factory=MangaProviders)
    volume_chapter_resolution: str = "manual_review_default"


class PaperlessSettings(BaseModel):
    enabled: bool = False
    base_url: str = "http://paperless:8000"
    token_env: str = "PAPERLESS_TOKEN"
    webhook_secret_env: str = "PAPERLESS_WEBHOOK_SECRET"
    import_document_types: list[str] = ["book_scan", "receipt_of_purchase", "book_invoice"]


class PreviewSettings(BaseModel):
    gotenberg_enabled: bool = False
    gotenberg_url: str = "http://gotenberg:3000"
    timeout_seconds: int = 30


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="BOOKAUDIT_",
        env_nested_delimiter="__",
        extra="ignore",
        populate_by_name=True,
    )

    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls: type[BaseSettings],  # noqa: ARG003 - required by pydantic-settings
        init_settings: PydanticBaseSettingsSource,
        env_settings: PydanticBaseSettingsSource,
        dotenv_settings: PydanticBaseSettingsSource,
        file_secret_settings: PydanticBaseSettingsSource,
    ) -> tuple[PydanticBaseSettingsSource, ...]:
        """Make environment and secret-file values override YAML/init data."""
        return env_settings, file_secret_settings, init_settings, dotenv_settings

    profile: str = "default"
    library: LibrarySettings = Field(default_factory=_default_library)
    storage: StorageSettings = Field(default_factory=_default_storage)
    database: DatabaseSettings = Field(default_factory=DatabaseSettings)  # type: ignore[arg-type]
    queue: QueueSettings = Field(default_factory=QueueSettings)  # type: ignore[arg-type]
    rate_limits: RateLimitSettings = Field(default_factory=RateLimitSettings)  # type: ignore[arg-type]
    vectors: VectorSettings = Field(default_factory=VectorSettings)  # type: ignore[arg-type]
    providers: ProviderSettings = Field(default_factory=ProviderSettings)  # type: ignore[arg-type]
    privacy: PrivacySettings = Field(default_factory=PrivacySettings)  # type: ignore[arg-type]
    extractors: ExtractorSettings = Field(default_factory=ExtractorSettings)  # type: ignore[arg-type]
    paperless: PaperlessSettings = Field(default_factory=PaperlessSettings)  # type: ignore[arg-type]
    preview: PreviewSettings = Field(default_factory=PreviewSettings)
    manga_mode: MangaSettings = Field(default_factory=MangaSettings)
    model_config_path: Path = Field(default=Path("config/models.yml"), alias="routing__model_config")

    open_ai_api_key: str | None = Field(
        None,
        validation_alias=AliasChoices("BOOKAUDIT_OPENAI_API_KEY", "OPENAI_API_KEY"),
    )
    open_ai_base_url: str = Field(
        "https://api.openai.com/v1",
        validation_alias=AliasChoices("BOOKAUDIT_OPENAI_BASE_URL", "OPENAI_BASE_URL"),
    )
    ollama_enabled: bool = Field(
        True,
        validation_alias=AliasChoices("BOOKAUDIT_OLLAMA_ENABLED", "ollama_enabled"),
    )
    ollama_base_url: str = Field(
        "http://localhost:11434/v1",
        validation_alias=AliasChoices("BOOKAUDIT_OLLAMA_BASE_URL", "OLLAMA_BASE_URL"),
    )
    gemini_api_key: str | None = Field(
        None,
        validation_alias=AliasChoices("BOOKAUDIT_GEMINI_API_KEY", "GEMINI_API_KEY"),
    )
    lmstudio_enabled: bool = Field(
        False,
        validation_alias=AliasChoices("BOOKAUDIT_LMSTUDIO_ENABLED", "lmstudio_enabled"),
    )
    lmstudio_base_url: str = Field(
        "http://localhost:1234/v1",
        validation_alias=AliasChoices("BOOKAUDIT_LMSTUDIO_BASE_URL", "LMSTUDIO_BASE_URL"),
    )
    judge_model: str = "gemma4:e4b-it-q4_K_M"
    vision_model: str = "gemma4:e4b-it-q4_K_M"
    log_level: str = "INFO"
    api_key: SecretStr | None = Field(
        None,
        validation_alias=AliasChoices("BOOKAUDIT_API_KEY", "api_key"),
    )

    # Flat aliases for common Docker env vars
    library_path_env: Path | None = Field(None, validation_alias=AliasChoices("BOOKAUDIT_LIBRARY_PATH"))
    db_path_env: Path | None = Field(None, validation_alias=AliasChoices("BOOKAUDIT_DB_PATH"))
    artifacts_dir_env: Path | None = Field(None, validation_alias=AliasChoices("BOOKAUDIT_ARTIFACTS_DIR"))
    read_only_env: bool | None = Field(
        None,
        validation_alias=AliasChoices("BOOKAUDIT_READ_ONLY"),
    )
    allow_remote_file_upload: bool = Field(
        False,
        validation_alias=AliasChoices("BOOKAUDIT_ALLOW_REMOTE_FILE_UPLOAD"),
    )


def load_settings(config_path: Path | None = None) -> Settings:
    if config_path is None:
        default_path = Path("config/config.yml")
        if default_path.exists():
            config_path = default_path

    yaml_config: dict[str, Any] = {}
    if config_path and config_path.exists():
        with open(config_path) as f:
            yaml_config = yaml.safe_load(f) or {}

    # Merge YAML config into settings
    settings = Settings(**yaml_config)

    # Apply flat env overrides if they exist
    if settings.library_path_env:
        settings.library.path = settings.library_path_env
    if settings.db_path_env:
        settings.storage.sqlite_path = settings.db_path_env
    if settings.artifacts_dir_env:
        settings.storage.artifacts_dir = settings.artifacts_dir_env
    if settings.read_only_env is not None:
        settings.library.read_only = settings.read_only_env

    return settings
