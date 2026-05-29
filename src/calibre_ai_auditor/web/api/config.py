from pathlib import Path
from typing import Any

import yaml
from fastapi import APIRouter, Body

from calibre_ai_auditor.config.settings import load_settings

router = APIRouter()


def sanitize_config(settings_dict: dict[str, Any]) -> dict[str, Any]:
    # We construct a clean dict with only allowed fields
    sanitized = {
        "profile": settings_dict.get("profile"),
        "allow_remote_file_upload": settings_dict.get("allow_remote_file_upload"),
        "log_level": settings_dict.get("log_level"),
        "judge_model": settings_dict.get("judge_model"),
        "vision_model": settings_dict.get("vision_model"),
    }
    
    # Library
    if "library" in settings_dict:
        sanitized["library"] = {
            "path": settings_dict["library"].get("path"),
            "read_only": settings_dict["library"].get("read_only"),
        }
        
    # Database
    if "database" in settings_dict:
        sanitized["database"] = {
            "backend": settings_dict["database"].get("backend"),
        }
        
    # Queue
    if "queue" in settings_dict:
        sanitized["queue"] = {
            "backend": settings_dict["queue"].get("backend"),
        }
        
    # Rate limits
    if "rate_limits" in settings_dict:
        sanitized["rate_limits"] = {
            "enabled": settings_dict["rate_limits"].get("enabled"),
            "backend": settings_dict["rate_limits"].get("backend"),
        }
        
    # Vectors
    if "vectors" in settings_dict:
        sanitized["vectors"] = {
            "enabled": settings_dict["vectors"].get("enabled"),
            "collection": settings_dict["vectors"].get("collection"),
            "embedding_provider": settings_dict["vectors"].get("embedding_provider"),
            "embedding_model": settings_dict["vectors"].get("embedding_model"),
            "local_only": settings_dict["vectors"].get("local_only"),
        }
        
    # Providers
    if "providers" in settings_dict:
        sanitized["providers"] = settings_dict["providers"].copy() if settings_dict["providers"] else {}
        
    # Privacy
    if "privacy" in settings_dict:
        sanitized["privacy"] = settings_dict["privacy"].copy() if settings_dict["privacy"] else {}
        
    # Extractors
    if "extractors" in settings_dict and "tika" in settings_dict["extractors"]:
        tika = settings_dict["extractors"]["tika"]
        sanitized["extractors"] = {
            "tika": {
                "enabled": tika.get("enabled"),
                "timeout_seconds": tika.get("timeout_seconds"),
                "write_limit": tika.get("write_limit"),
                "max_embedded_resources": tika.get("max_embedded_resources"),
            }
        }
        
    # Paperless
    if "paperless" in settings_dict:
        sanitized["paperless"] = {
            "enabled": settings_dict["paperless"].get("enabled"),
            "import_document_types": settings_dict["paperless"].get("import_document_types"),
        }
        
    # Preview
    if "preview" in settings_dict:
        sanitized["preview"] = {
            "gotenberg_enabled": settings_dict["preview"].get("gotenberg_enabled"),
            "timeout_seconds": settings_dict["preview"].get("timeout_seconds"),
        }
        
    # Manga Mode
    if "manga_mode" in settings_dict:
        sanitized["manga_mode"] = settings_dict["manga_mode"].copy() if settings_dict["manga_mode"] else {}
        
    return sanitized


@router.get("/config")
async def get_config() -> dict[str, Any]:
    settings = load_settings()
    return sanitize_config(settings.model_dump())


@router.post("/config")
async def save_config(payload: dict[str, Any] = Body(...)) -> dict[str, Any]:
    config_path = Path("config/config.yml")
    yaml_config: dict[str, Any] = {}
    if config_path.exists():
        with open(config_path) as f:
            yaml_config = yaml.safe_load(f) or {}

    if "log_level" in payload:
        yaml_config["log_level"] = payload["log_level"]
    if "privacy" in payload:
        if "privacy" not in yaml_config:
            yaml_config["privacy"] = {}
        for k, v in payload["privacy"].items():
            yaml_config["privacy"][k] = v
    if "providers" in payload:
        if "providers" not in yaml_config:
            yaml_config["providers"] = {}
        for k, v in payload["providers"].items():
            yaml_config["providers"][k] = v

    config_path.parent.mkdir(parents=True, exist_ok=True)
    with open(config_path, "w") as f:
        yaml.safe_dump(yaml_config, f, default_flow_style=False)

    settings = load_settings(config_path)
    return sanitize_config(settings.model_dump())
