# Pushing to self-hosted Gitea

The canonical development remote is the homelab Gitea repository. The local
checkout currently names it `gitea` and uses
`http://192.168.0.122:3010/felix/calibre-ai-auditor.git`.

**You need to push the branch from a machine that has access to your
homelab Gitea** (the Unraid box itself, or any machine with the right
SSH key in its agent).

## One-time setup (from a machine with Gitea access)

```bash
# 1. Clone the Gitea repo (if not already cloned)
git clone http://192.168.0.122:3010/felix/calibre-ai-auditor.git
cd calibre-ai-auditor

# 2. Fetch and check out the intended Gitea branch
git fetch origin
git checkout <branch>

# 3. Existing checkouts can add the canonical Gitea remote
git remote add gitea http://192.168.0.122:3010/felix/calibre-ai-auditor.git

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
2. **`.gitea/workflows/v1-tests.yml`** runs the 4 jobs:
   - `backend` — ruff + mypy + pytest (no benchmarks)
   - `benchmarks` — pytest --benchmark-only on push
   - `webui-lint-build` — npm install + lint + build
   - `webui-e2e` — Playwright E2E suite

3. **Workflow artifacts** uploaded:
   - `coverage-xml`
   - `benchmark-baseline` (`.benchmarks/baseline.json`)
   - `playwright-report` (HTML report of the E2E run)

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
    "title": "release(v1.0): Content-Ground Verification",
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
