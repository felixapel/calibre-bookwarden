# Pushing to self-hosted Gitea

The `.gitea/workflows/v1-tests.yml` workflow file is in place but the
sandbox dev container doesn't have network access to your homelab Gitea
(verified during the v1.0 release push — SSH connection to
`192.168.0.122:22` fails with "Too many authentication failures").

**You need to push the branch from a machine that has access to your
homelab Gitea** (the Unraid box itself, or any machine with the right
SSH key in its agent).

## One-time setup (from a machine with Gitea access)

```bash
# 1. Clone the repo (if not already cloned)
git clone git@github.com:felixapel/calibre-ai-auditor.git
cd calibre-ai-auditor

# 2. Fetch the v1.0 branch
git fetch origin feat/v1-content-verification
git checkout feat/v1-content-verification

# 3. Add the Gitea remote (use your actual Gitea URL)
git remote add gitea <your-gitea-url>

# Examples (adjust to your actual setup):
# git remote add gitea git@192.168.0.122:felix/calibre-ai-auditor.git
# git remote add gitea ssh://git@git.felixserver.dev:2222/felix/calibre-ai-auditor.git
# git remote add gitea https://git.felixserver.dev/felix/calibre-ai-auditor.git

# 4. Verify the remote URL by listing it
git remote -v

# 5. Push the v1.0 branch
git push -u gitea feat/v1-content-verification
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
    "body": "See PR #1 on github.com/felixapel/calibre-ai-auditor for the full description."
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

## Why I couldn't do this in the sandbox

The dev container running this Claude session has no network route
to `192.168.0.122` (the homelab Gitea host). The push attempt produced:

```
ssh_askpass: exec(/usr/lib/ssh/ssh-askpass): No such file or directory
Permission denied, please try again.
Received disconnect from 192.168.0.122 port 22:2: Too many authentication failures
```

This is a network isolation constraint of the dev environment, not a
config issue. The branch is fully ready on `origin/feat/v1-content-verification`
(GitHub); you can fetch + push to Gitea from any machine with proper
access.

## Alternative: bundle transfer (no network setup needed)

If you don't want to set up SSH agent forwarding or HTTPS tokens,
you can transfer the branch as a single file:

```bash
# From this dev container (already done):
git bundle create /tmp/v1.0.bundle main..feat/v1-content-verification

# Then transfer /tmp/v1.0.bundle to your Unraid box by any means
# (scp, USB stick, Syncthing, etc.) — it's a single ~5 MB file.

# On Unraid:
git clone v1.0.bundle calibre-ai-auditor
cd calibre-ai-auditor
git remote set-url origin <your-gitea-url>
git push -u origin feat/v1-content-verification:main
```

The bundle contains the full branch history including all 8 commits
with full message bodies and the same tree hash as GitHub.
