import asyncio
import json

from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.judge.engine import MetadataJudge
from calibre_ai_auditor.storage.models import EvidencePackage


async def main():
    settings = load_settings()
    judge = MetadataJudge(settings)
    
    package = EvidencePackage(
        book_key="test",
        run_id="test",
        current={"title": "Alternating Current", "authors": ["Octavio Paz"]},
        extracted={"title": "Alternating Current", "authors": ["Octavio Paz"]},
        candidates=[],
        snippets=[{"source": "test", "text": "This is a book by Octavio Paz about literature."}],
        risk_flags=[]
    )
    
    print(f"Calling judge with model: {settings.judge_model}")
    try:
        result = await judge.judge(package)
        print("Judge Result:")
        print(json.dumps(result, indent=2))
    except Exception as e:
        print(f"Judge failed: {e}")

if __name__ == "__main__":
    asyncio.run(main())
