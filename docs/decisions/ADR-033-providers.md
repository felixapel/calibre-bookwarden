# ADR-033: Agent seats are providers with their own egress, family and quota state

- Status: Accepted
- Date: 2026-10-29
- Decision: Felix's
- Amends: [ADR-029](ADR-029-agent-fixing.md)

## Context

The agent panel's three seats share one HTTP client. It allows only https, and it refuses private, loopback and link-local addresses at dial time. That is right for the three public APIs, but an operator may want a seat to use an OpenAI-compatible gateway on the same network (plain http with a static key, serving models from several vendors), or a local vLLM on that network. Both are plain http on private addresses, so today they are refused.

Two other gaps:
- Nothing checks that the panel's seats come from different vendors. A seat pointed at a proxy could silently make two judges the same model, which removes the value of a unanimous panel.
- A seat that ran out of credits or hit a rate limit looks like any other failure, so Settings can't say why a judge keeps leaving books.

## Decision

1. **Egress is opted into per seat, by exact address.** `BOOKWARDEN_AGENT_<SEAT>_ALLOW_CIDR` is a comma-separated list of prefixes: /32 for IPv4, /128 for IPv6. Anything wider is a config error. With it set, the seat's URL may be `http://` or name a private address, but every address the seat dials must be inside the list; nothing else is reachable. Without it, the old rules hold: https only, and no private or special-purpose address.
2. **One HTTP client per seat.** Each seat has its own client, with its own allowlist (the host:port of its URL) and its own dial-time check, so an allowance never reaches another seat. Redirects are never followed.
3. **Special-purpose ranges are refused at dial time** unless an exact prefix allows the address: 0.0.0.0/8, CGNAT 100.64.0.0/10, 192.0.0.0/24, the documentation ranges, benchmarking 198.18.0.0/15, 240.0.0.0/4, IPv6 ULA, Teredo and the IETF block 2001::/23, IPv4-compatible ::/96, site-local fec0::/10, 64:ff9b::/96 and 64:ff9b:1::/48 (NAT64) and 2002::/16, on top of loopback, private, link-local, multicast and unspecified. Any IPv6 address with a zone is refused, because a zone defeats the prefix checks, and a seat URL may not name one. The check is `internal/netguard`, shared with the source client.
4. **A seat on plain http logs a startup warning** naming the seat: its key travels unencrypted. The key is never logged.
5. **Each seat has a model family**, `BOOKWARDEN_AGENT_<SEAT>_FAMILY`. The default is `anthropic` for haiku, `deepseek` for deepseek and `google` for gemini. The allowed values are anthropic, deepseek, google, openai, qwen, moonshot, minimax and zhipu. Two configured seats with the same family are a config error. Settings shows the family beside each model.
6. **Quota state.** A seat call that gets HTTP 429, or a 402 or 403 with a JSON body (`Content-Type` contains json) whose `error.message`, `error.type`, `error.code` or top-level `message` contains quota, credits, billing or "rate limit" (case-insensitive, a closed list), fails with the code `quota_exhausted` instead of `rate_limited`, `http_4xx`. A 429 is still retried up to three times first. The `Retry-After` header is recorded when present. The verdict stores `quota_exhausted` as its error code, and the book is left for the admin as with any failed seat. Settings shows each seat's last exhaustion time and retry-after, kept in memory: it is empty after a restart, a successful call clears it, and no table is added.
7. **Secrets.** The Calibre password loses only a trailing CR or LF (a secrets file's line ending) and keeps its spaces; if it still starts or ends with a space, a startup warning names `BOOKWARDEN_CALIBRE_PASSWORD`, never the value. The `*_KEY` variables are trimmed, since HTTP trims header values anyway and a stray CR would break the request. A blank value counts as unset. Other values keep trimming.
8. **Defence in depth.** A seat URL needs a host. When it names an IP address and `_ALLOW_CIDR` is set, that address must be inside it. `NewSeat` and `NewSeatClient` refuse plain http without an allowance.

## Consequences

- A key sent over plain http can be sniffed on the LAN. Felix accepts that per seat by setting its CIDR, and the startup warning keeps it visible.
- Pointing a seat at a proxy doesn't change which vendor judges unless its family says so. Family is declared, not detected, so a wrong value defeats the check; it guards against a mistake, not a lie.
- Per-provider budgets are deferred to M4, when the extractors arrive.
- Extractor roles (v2, M4) reuse this layer: a provider, its egress rules, family and quota state.
- Live probes of the proxy and of local models (S2, S3) are `-tags live`, and run only when their variables are set. Tests never call a real model.
