# License Recommendation

## Recommended license

**GPL-3.0-or-later**

SPDX:

```text
GPL-3.0-or-later
```

## Why this is the safest choice

This project is likely to live close to the Calibre ecosystem.

Reasons:

- Calibre itself is GPLv3
- a later thin Calibre plugin is plausible
- future contributors may want to import Calibre Python APIs directly
- studying or adapting GPL ecosystem code is easier when the repo is already GPL-compatible

## When to reconsider

Use a permissive license such as Apache-2.0 **only if** the project commits to:

- subprocess-only integration with Calibre
- no direct imports from Calibre Python modules
- no GPL code reuse from ecosystem projects

## Practical recommendation

For the repo bootstrap:

1. keep this `LICENSE.md`
2. before the first public release, add the canonical GPL text as `LICENSE`
3. add SPDX headers to source files

Example header:

```python
# SPDX-License-Identifier: GPL-3.0-or-later
```