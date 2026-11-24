# ADR-013: Retire the V1 CLI pipeline

- Status: Superseded by [ADR-015](ADR-015-go-rewrite.md).
- Date: 2026-10-05
- Decision owners: maintainer

## Context

After ADR-012, the only HTTP surface is Certificate A and the supervised
writer takes work from the operator CLI. The `bookaudit` CLI still carried the
pre-V2 pipeline: `scan`, `inspect`, `audit` (LLM judge), `verify --pipeline v1`,
`audit-360`, `ingest-paperless`, `hosts`, a direct non-production `undo`, and
five commands that only printed that they were retired (`apply`,
`optimize-covers`, `sync-library`, `curate-periodicals`, `full-audit-run`).
They kept about 2.4k lines alive outside every supported path: the V1 audit
engine, `DirectCalibreEngine`, comics, the Paperless/Calibre-Web/homelab
bridges, the provider registry and the Qdrant vector store, plus the
`qdrant-client`/gRPC dependency in the writer image.

## Decision

Remove those commands and every module that no entrypoint reaches any more
(static import graph from `cli.main`, `production_cli` and
`web.production`). `verify` keeps only the Manifestation V2 shadow pipeline;
its `--pipeline` option is gone.

The CLI keeps Certificate A and its verifier, local V2 `verify`, read-only
Content Server `inventory` and `verify-content-server`, `calibrate-v2`, the
writer, its operator commands (`authorize`, `queue-apply`, `queue-undo`,
`operation`, `pilot-stop`, `incident-ack`), `retention`, `migrate`, `doctor`
and `config`. V2 still uses the LLM, OCR-vision and cover-forensics modules as
non-authoritative evidence, so they stay.

## Consequences

Scripts that call a removed command or `--pipeline` fail with a usage error.
Paperless ingest, homelab host discovery, 360° forensics and the V1 judge are
no longer available. Re-adding any of them needs a new decision with a V2
contract (ADR-003). The writer image no longer installs `qdrant-client` or gRPC.
