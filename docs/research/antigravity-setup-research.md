# Antigravity Setup Research: Repositories, Extensions, MCPs, and Workflow Patterns

This document provides a security-aware, highly practical curation of tools and configurations to make Google Antigravity more powerful, resilient, and safe for advanced, local-first code orchestration.

---

## 1. Core Extensions Curation (Open VSX Compatible)

### A. Python Development (Ruff Extension)
*   **VSX Identifier**: `charliermarsh.ruff`
*   **GitHub**: `https://github.com/astral-sh/ruff-vscode`
*   **Metrics**: 5,000+ stars on GitHub; highly active.
*   **License**: MIT.
*   **Security Concerns & Permissions**: Runs local executable compilation loops. No network access or cloud data transmission. Safe.
*   **Why it's useful**: Extremely fast linting and formatting (replaces flake8, black, and isort) to format python files instantly in agentic loops without high execution overhead.

### B. REST Client (API Testing)
*   **VSX Identifier**: `humao.rest-client`
*   **GitHub**: `https://github.com/Huachao/vscode-restclient`
*   **Metrics**: 4,200+ stars; actively maintained.
*   **License**: MIT.
*   **Security Concerns & Permissions**: Executes HTTP/HTTPS requests from the local host. Needs network permissions.
*   **Useful but dangerous**: Can send authorization headers and secret API keys to arbitrary internet servers if a developer runs a compromised `.http` file.
*   **Why it's useful**: Allows agents to run and test FastAPI REST routes directly from plain-text `.http` files inside the workspace, avoiding bulky, cloud-synced Postman tools.

### C. GitLens (Git History)
*   **VSX Identifier**: `eamodio.gitlens`
*   **GitHub**: `https://github.com/gitkraken/vscode-gitlens`
*   **Metrics**: 8,000+ stars; highly active.
*   **License**: Custom (includes premium cloud subscriptions).
*   **Useful but dangerous**: The newer versions default to connecting to GitKraken's remote cloud platform for team features.
*   **Why it's useful**: Displays rich inline author blame, history, and commit navigation. For Antigravity, we recommend disabling its cloud features or using the community version to prevent local source metadata leakage.

---

## 2. Model Context Protocol (MCP) Curation

### A. Filesystem MCP (Reference Server)
*   **GitHub**: `https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem`
*   **License**: MIT.
*   **Security Concerns**: Exposes local path read/write access.
*   **Useful but dangerous**: An agent can read or delete files anywhere on the permitted path.
*   **Curation Strategy**: Strict scope restriction. Restrict Filesystem MCP configurations to the project workspace directory only; never allow root-level access.

### B. SQLite MCP
*   **GitHub**: `https://github.com/modelcontextprotocol/servers/tree/main/src/postgres` or `src/sqlite`
*   **License**: MIT.
*   **Security Concerns**: Executes database writes, updates, and deletes.
*   **Useful but dangerous**: Can drop tables or corrupt data.
*   **Curation Strategy**: In testing, mount databases as read-only where possible, or use transaction-scoped connections.

### C. Docker MCP
*   **GitHub**: `https://github.com/modelcontextprotocol/servers` (community implementations)
*   **License**: MIT.
*   **Security Concerns**: Executes container startup, rebuild, and teardown commands.
*   **Useful but dangerous**: Allows arbitrary control over container lifecycles.
*   **Curation Strategy**: Restrict access to designated project-labeled containers only.

---

## 3. Workflow & Prompt Management Patterns

### A. System Rule files (`.cursorrules` / `.clinerules`)
Placing explicit rules inside the workspace root prevents the agent from making architectural deviations:
*   **Rule Set**: Set styling guidelines (e.g. "Use vanilla CSS, no Tailwind unless asked"), performance instructions (e.g. "Always write SQLite upserts to prevent key constraint violations"), and model routing maps.

### B. Git Automation Pre-Commit Hooks
*   Injecting `ruff check` and `pytest` checks into `.git/hooks/pre-commit` prevents broken commits from being pushed to master.

---

## 4. Specific Curation Matrix

| Focus Area | Tool / Repo | Security Class | Compatibility | Action / Verdict |
| :--- | :--- | :---: | :---: | :--- |
| **Code Intelligence** | Ruff VS Code Extension | Safe | High | **Adopt** |
| **Testing** | REST Client Extension | **Useful but dangerous** | High | **Adopt (restrict API keys)** |
| **Database** | SQLite MCP Server | **Useful but dangerous** | High | **Adopt (scope to local `.db`)** |
| **Context Memory** | Memory MCP Server | Safe | High | **Adopt** |
| **Containers** | Docker Extension | Safe | High | **Adopt** |
| **VCS** | GitLens Extension | **Useful but dangerous** | High | **Adopt (disable cloud sync)** |
