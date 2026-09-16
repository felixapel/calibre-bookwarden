"""Static contracts for the GitHub release artifact boundary."""

from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
RELEASE = ROOT / ".github" / "workflows" / "release.yml"
CI = ROOT / ".github" / "workflows" / "ci.yml"


def test_release_publishes_each_runtime_target_by_immutable_digest() -> None:
    workflow = RELEASE.read_text()

    for target in ("certificate-a", "writer", "caddy-edge"):
        assert f"target: {target}" in workflow
    for suffix in ('image_suffix: ""', "image_suffix: -writer", "image_suffix: -edge"):
        assert suffix in workflow
    assert "push-by-digest=true" in workflow
    assert "name-canonical=true" in workflow
    assert "provenance: mode=max" in workflow
    assert "sbom: true" in workflow
    assert "actions/attest-build-provenance@" in workflow
    assert "actions/attest-sbom@" in workflow
    assert 'cosign sign --yes "$IMAGE"' in workflow
    assert "cosign verify" in workflow
    assert "--certificate-oidc-issuer https://token.actions.githubusercontent.com" in workflow
    assert "Emit immutable image receipt" in workflow
    assert "name: release-image-${{ matrix.name }}-${{ github.ref_name }}" in workflow
    assert '"digest":"%s"' in workflow
    assert 'docker buildx imagetools create --tag "$image:$version" --tag "$image:$minor" "$image@$digest"' in workflow


def test_release_binds_tag_version_and_images_to_main_ancestral_source() -> None:
    workflow = RELEASE.read_text()

    assert 'test "$GITHUB_REF_NAME" = "v$version"' in workflow
    assert "git merge-base --is-ancestor" in workflow
    assert 'test "$GITHUB_SHA" = "$reviewed_revision"' in workflow
    assert "org.opencontainers.image.revision=${{ steps.source.outputs.revision }}" in workflow
    assert "org.opencontainers.image.version=${{ github.ref_name }}" in workflow
    assert "org.opencontainers.image.source=${{ github.server_url }}/${{ github.repository }}" in workflow
    assert "Verify the published digest's OCI provenance and runtime boundary" in workflow
    assert "statuses: read" in workflow
    assert "verify_gitea_canonical_status.py" in workflow
    assert "--creator felixapel" in workflow


def test_release_runs_the_executable_gate_and_smokes_certificate_a_only() -> None:
    workflow = RELEASE.read_text()

    assert "./scripts/verify-calibre-gate.sh" in workflow
    assert "if: matrix.target == 'certificate-a'" in workflow
    assert "docker compose --profile maintenance run --rm provision-roles" in workflow
    assert "docker compose --profile maintenance run --rm migrate" in workflow
    assert "docker compose config -q" in workflow
    assert "docker compose up -d --wait verifier app" in workflow
    assert 'BOOKAUDIT_IMAGE="$IMAGE@$DIGEST"' in workflow
    assert "test ! -e /opt/calibre/calibredb" in workflow
    assert "permission denied for table operationledger" in workflow
    assert "BOOKAUDIT_WRITER_POSTGRES_DSN=postgresql+psycopg://bookaudit_writer:" in workflow


def test_github_ci_builds_the_same_explicit_runtime_boundaries() -> None:
    workflow = CI.read_text()

    assert "target: certificate-a" in workflow
    assert "docker build --target writer" in workflow
    assert "docker build --target caddy-edge" in workflow
    assert "BOOKAUDIT_BUILD_REVISION=${{ github.sha }}" in workflow
    fixture = (ROOT / "ops" / "monitoring" / "Dockerfile.ci").read_text(encoding="utf-8")
    assert "ops/monitoring/Dockerfile.ci" in workflow
    assert (
        "FROM prom/prometheus:v3.13.3@sha256:6976aa8a60fec930796ce5772b8d12da7a318a5daa8d40d69c5c7819a05eeed7"
        in fixture
    )
    assert 'test "$(date -u +%F)" \\< "2026-10-17"' in workflow
    assert "docker run --rm calibre-ai-auditor:ci --help" in workflow
    assert "docker run --rm --entrypoint bookaudit calibre-ai-auditor:ci retention" not in workflow
    assert "ops/monitoring/Dockerfile.ci" in workflow
    assert "ci_compose_set_project_from_run_id" in workflow
    assert "docker compose up -d --wait verifier app" in workflow


def test_release_promotes_only_after_all_three_signed_receipts_are_validated() -> None:
    workflow = RELEASE.read_text()

    aggregate = workflow.split("\n  promote-images:\n", 1)[1]
    assert "needs: publish-images" in aggregate
    assert "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093" in aggregate
    assert "release requires exactly one validated receipt for each runtime image" in aggregate
    assert "name: release-images-${{ github.ref_name }}" in aggregate
    assert 'docker buildx imagetools create --tag "$image:$version" --tag "$image:$minor" "$image@$digest"' in aggregate
    assert "Promote the fully gated digest to semver tags" not in workflow.split("\n  promote-images:\n", 1)[0]


def test_github_ci_compose_smoke_uses_the_provisioned_database_roles() -> None:
    workflow = CI.read_text()

    for dsn in (
        "postgresql+psycopg://bookaudit_app:app@postgres/bookaudit",
        "postgresql+psycopg://bookaudit_verifier:verifier@postgres/bookaudit",
        "postgresql+psycopg://bookaudit_writer:writer@postgres/bookaudit",
        "postgresql+psycopg://bookaudit_migrator:migrator@postgres/bookaudit",
    ):
        assert dsn in workflow


def test_release_exception_expiry_is_checked_before_each_irreversible_stage() -> None:
    workflow = RELEASE.read_text()

    guard = 'test "$(date -u +%F)" \\< "2026-10-17"'
    assert workflow.count(guard) >= 3
    publish = workflow.split("\n  publish-images:\n", 1)[1].split("\n  promote-images:\n", 1)[0]
    promote = workflow.split("\n  promote-images:\n", 1)[1]
    assert guard in publish
    assert guard in promote
