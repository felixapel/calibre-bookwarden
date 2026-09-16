# Calibre Bookwarden

A local Calibre metadata auditor that compares records with ebook contents, records evidence and explains uncertainty.

## Current safety boundary

The supported production profile is **Certificate A: stopped-library, read-only verification**. The development application has a larger historical surface; it is not a substitute for the production entrypoint. Gitea is the canonical development and validation pipeline.

Local remediation is tracked in [the plan](docs/REMEDIATION_PLAN.md) and [validation results](docs/REMEDIATION_RESULTS.md). This working tree has not been promoted by exact-revision Linux/service/image gates. No zero-risk, perfect-accuracy or production-readiness claim is made.

## Available inspection

- Manifestation V2 inspects attached formats, resolves identity from evidence and seals its findings.
- Direct SQLite auditing reports integrity issues, missing files, suspicious titles, author-sort differences and cover diagnostics. It cannot write to the library.
- Cover scores and optional vision output are review evidence, not authority to replace images.
- Duplicate analysis distinguishes verified byte matches from possible editions; matching titles or complementary formats do not authorize merging.
- The production SPA exposes verification and sealed evidence review. The development Cover Deck is read-only and does not apply covers.

The legacy commands `optimize-covers`, `sync-library`, `curate-periodicals` and `full-audit-run` are retired and reject before mutation. The direct mutation endpoints, experimental ledger, JSON writer lock and prototype cache/reconnect actions are also disabled. Setting `read_only=false` does not re-enable them.

## Local inspection

From an already checked-out, trusted repository with its locked environment:

```sh
uv run bookwarden audit-360 --library "/path/to/stopped-or-restored-library"
```

Audit output can contain titles, paths and other private metadata; keep it local. A successful SQLite integrity check is not proof of correct metadata, complete ebook contents or a recoverable whole-library backup.

For isolated mechanism checks, use the [disposable Calibre lab](docs/runbooks/disposable-calibre-lab.md). Production setup and supported entrypoints are described in the [operations runbook](docs/runbooks/production-operations.md); do not deploy an unreviewed development web command in their place.

## Supervised corrections

The existing Manifestation V2 coordinator and sole writer retain exact manual authorization, sealed artifacts, serialization, before/after verification and recovery checks. Certificate A does not enable that writer. A supported write pilot requires its dedicated real-service gates, exclusion of external writers, a tested whole-library restore and explicit operator authorization.

The experimental candidate scorer cannot promote itself to Tier A or auto-apply. The authoritative resolver remains `verification/identity_v2.py`. An ISBN match, OCR result or attractive cover alone is insufficient.

Database snapshots use SQLite's consistent backup mechanism and are **database-only**. They do not back up ebook formats, covers or OPFs, and never authorize a mutation. No fallback copies only a live main database while ignoring its WAL.

SQLite paths are opened with an escaped URI and `mode=ro`. UNC shares require a SQLite build supporting `SQLITE_ALLOW_URI_AUTHORITY`; unsupported builds fail closed without a writable fallback. No live UNC share was verified. See the [SQLite URI documentation](https://www.sqlite.org/uri.html).

On Windows, the legacy DirectEngine file/cover inspection and CoverDeck image reads are unavailable (`UNSUPPORTED_SECURE_FILE_READ` / unavailable image). They fail closed because the current secure-file implementation cannot guarantee descriptor-anchored containment there. SQLite metadata-only inspection remains read-only. Run file/cover inspection in the supported Linux environment; this remediation does not claim Windows feature parity.

## Performance and validation

Performance depends on corpus, storage, formats, OCR and providers. Earlier unverified throughput/RAM figures are not release guarantees. The metadata-only benchmark excludes scan I/O, OCR, network and persistence; report its scope with any result.

Follow [AGENTS.md](AGENTS.md) and [TESTING.md](TESTING.md). Linux, real Calibre/OCR, PostgreSQL/Valkey, browser and image gates remain distinct. Mocked browser tests and dependency audits cannot substitute for them.

## 💖 Supporting & Sponsoring

Calibre Bookwarden is an independent open-source project dedicated to digital preservation, content-grounded media verification, and homelab sovereignty.

If this project saved your library from corruption, upgraded your covers, or saved you hours of manual editing, please consider supporting continued development:

<p align="center">
  <a href="https://github.com/sponsors/felixapel">
    <img src="https://img.shields.io/badge/Sponsor%20on-GitHub%20Sponsors-EA4AAA?style=for-the-badge&logo=githubsponsors&logoColor=white" alt="GitHub Sponsors">
  </a>
  &nbsp;&nbsp;
  <a href="https://ko-fi.com/felixapel">
    <img src="https://img.shields.io/badge/Support%20on-Ko--fi-FF5E5B?style=for-the-badge&logo=kofi&logoColor=white" alt="Support on Ko-fi">
  </a>
</p>

### Where does funding go?
- 🖥️ **Homelab Test Hardware**: Maintaining real unRAID, TrueNAS, and multi-GPU testing rigs for local OCR and vision models.
- 🔬 **Ebook Forensics Research**: Adding deep container parsers for obscure formats (MOBI PalmDOC, DjVu, CBZ comic metadata).
- ☕ **Open Source Sustainability**: Keeping the project 100% telemetry-free, ad-free, and GPLv3 licensed.

---

## Documentation

- [CLI reference](CLI_REFERENCE.md)
- [Usage](USAGE.md)
- [Architecture](ARCHITECTURE.md)
- [Production operations](docs/runbooks/production-operations.md)
- [Disposable Calibre lab](docs/runbooks/disposable-calibre-lab.md)
- [Remediation plan](docs/REMEDIATION_PLAN.md)
- [Validation results](docs/REMEDIATION_RESULTS.md)
- [Architecture decisions](docs/decisions/)
- [Roadmap](ROADMAP.md)
- [Contributing](CONTRIBUTING.md)

## License

GNU General Public License v3.0 or later. See [LICENSE](LICENSE).
