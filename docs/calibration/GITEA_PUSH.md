# Pushing to self-hosted Gitea

The canonical development remote is the homelab Gitea repository. The local
checkout currently names it `origin` and uses
`ssh://git@192.168.0.122:2222/felix/calibre-bookwarden.git` (or HTTP `http://192.168.0.122:3010/felix/calibre-bookwarden.git`).

**You need to push the branch from a machine that has access to your
homelab Gitea** (the Unraid box itself, or any machine with the right
SSH key in its agent).

## One-time setup (from a machine with Gitea access)

```bash
# 1. Clone the Gitea repo (if not already cloned)
git clone http://192.168.0.122:3010/felix/calibre-bookwarden.git
cd calibre-bookwarden

# 2. Fetch and check out the intended Gitea branch
git fetch origin
git checkout <branch>

# 3. Existing checkouts can add the canonical Gitea remote
git remote add origin ssh://git@192.168.0.122:2222/felix/calibre-bookwarden.git

# Examples (adjust to your actual setup):
# git remote add gitea git@192.168.0.122:felix/calibre-ai-auditor.git
# git remote add gitea ssh://git@git.felixserver.dev:2222/felix/calibre-ai-auditor.git
# git remote add gitea https://git.felixserver.dev/felix/calibre-ai-auditor.git

# 4. Verify the remote URL by listing it
git remote -v

# 5. Push only when explicitly authorized
git push -u gitea <branch>
```

## What happens after the push

1. **Gitea webhook** (if configured) fires → **act_runner** picks up the workflow
2. **`.gitea/workflows/v1-tests.yml`** runs five jobs:
   - `backend` — ruff + mypy + PostgreSQL/Valkey pytest and dependency gates
   - `v2-pilot-integration` — required no-skip real Calibre/Tesseract apply,
     readback, queued undo and restored readback
   - `benchmarks` — pytest --benchmark-only on push
   - `webui` — npm audit/lint/build plus desktop/mobile Playwright
   - `container` — production image, Compose, Prometheus and Trivy contract

## Creating the Gitea PR (after push)

Use the Gitea web UI, or via the API:

```bash
GITEA_URL=https://git.felixserver.dev  # adjust
GITEA_TOKEN=<your-gitea-personal-access-token>

curl -X POST "$GITEA_URL/api/v1/repos/felix/calibre-ai-auditor/pulls" \
  -H "Authorization: token $GITEA_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "head": "feat/v1-content-verification",
    "base": "main",
    "title": "feat: supervised exact-manifestation V2 pilot",
    "body": "Implementation details, verification evidence, migration notes, and rollback plan."
  }'
```

## Tracking act_runner

```bash
# On Unraid, watch the act_runner logs:
ssh unraid 'docker logs -f act_runner 2>&1 | grep -E "v1-tests|feat/v1-content-verification"'

# Or check job status via Gitea API:
curl -H "Authorization: token $GITEA_TOKEN" \
  "$GITEA_URL/api/v1/repos/felix/calibre-ai-auditor/actions/runs?labels=v1-tests"
```

From a checkout whose `gitea` remote points at the canonical repository, prefer
the authenticated `tea` views for an auditable status check:

```bash
tea actions runs list --remote gitea
tea actions runs view <run-id> --remote gitea
tea actions runs logs <run-id> --job <job-id> --remote gitea
```

Only `Status: completed` together with `Conclusion: success` for the exact head
SHA is green evidence. A dependent job showing zero duration and no runner after
an upstream failure was not executed and must not be counted as passing. Keep
the original run and logs as failure evidence; a later authorized push should
trigger a fresh run naturally.

Do not assume push authorization from implementation work. Inspect the exact
branch, status, diff, commits, remote, and existing Gitea Actions first. Never
manually rerun Gitea Actions unless the operator explicitly requests it.

## Alternative: bundle transfer (no network setup needed)

If you don't want to set up SSH agent forwarding or HTTPS tokens,
you can transfer the branch as a single file:

```bash
# From the source checkout:
git bundle create /tmp/calibre-ai-auditor.bundle main..<branch>

# Then transfer /tmp/v1.0.bundle to your Unraid box by any means
# (scp, USB stick, Syncthing, etc.) — it's a single ~5 MB file.

# On Unraid:
git clone calibre-ai-auditor.bundle calibre-ai-auditor
cd calibre-ai-auditor
git remote set-url origin <your-gitea-url>
git push -u origin <branch>
```

The bundle preserves the selected branch history and can be verified locally
before any Gitea push.
