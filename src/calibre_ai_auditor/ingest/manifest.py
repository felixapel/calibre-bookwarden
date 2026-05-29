import json
from pathlib import Path


class IngestManifest:
    def __init__(self, manifest_path: Path):
        self.manifest_path = manifest_path
        self._seen: set[str] = set()
        self._load()

    def _load(self) -> None:
        if self.manifest_path.exists():
            try:
                data = json.loads(self.manifest_path.read_text())
                self._seen = set(data.get("seen_files", []))
            except json.JSONDecodeError:
                self._seen = set()

    def _save(self) -> None:
        self.manifest_path.parent.mkdir(parents=True, exist_ok=True)
        self.manifest_path.write_text(json.dumps({"seen_files": list(self._seen)}))

    def has_seen(self, file_path: Path) -> bool:
        return str(file_path.absolute()) in self._seen

    def mark_seen(self, file_path: Path) -> None:
        self._seen.add(str(file_path.absolute()))
        self._save()
