from typing import Any

from fastapi import APIRouter, BackgroundTasks, Depends

from calibre_ai_auditor.audit.engine import run_audit
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.web.jobs import start_job
from calibre_ai_auditor.web.schemas import APIResponse

router = APIRouter(prefix="/runs", tags=["Runs and Audit"])


def get_settings() -> Settings:
    return load_settings()


@router.post("/{run_id}/audit", response_model=APIResponse)
async def start_audit(
    run_id: str,
    background_tasks: BackgroundTasks,
    settings: Settings = Depends(get_settings),
) -> Any:
    # Trigger audit as a background job
    job_id = await start_job(f"audit:{run_id}", run_audit, settings, run_id)

    return {"status": "success", "data": {"job_id": job_id, "message": "Audit started"}}
