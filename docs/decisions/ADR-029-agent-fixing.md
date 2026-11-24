# ADR-029: A unanimous three-model panel may decide proposals (amended 2026-11-04: mode on retired)

- Status: Accepted; amended 2026-11-04: mode on retired (see the amendment at the end)
- Date: 2026-10-27
- Decision: Felix's, in the agent-fixing design (`docs/superpowers/specs/2026-10-27-agent-fixing-design.md`) and plan; amends ADR-021 and spec section 8

## Context

Until now a fix was written only after a person approved it (ADR-021). On
2026-10-26 the run-4 and run-5 proposals were reviewed by hand with LLMs,
each judge alone with the sealed evidence. Only what every judge called
correct was applied. The judges caught real mistakes: editors proposed as
authors, imprints swapped for their parent company, a dropped subtitle.

On 2026-10-27, DeepSeek V4.1, Gemini 3.8 Flash (High) and Haiku 5.5
re-judged all 532 applied fixes and agreed on 529. They unanimously
rejected 14 of run 5's 17 open proposals, and were unanimously unsure on
the other 3. Felix wants that review to run on its own: "if you 3 models
agree fix issues autonomously".

## Decision

**The panel.** There are three seats from three vendors, with no fourth
vote and no tie-break:

| Seat | Model | Wire format |
|---|---|---|
| `haiku` | Claude Haiku 5.5 | Anthropic Messages API |
| `deepseek` | DeepSeek V4.1 Flash | OpenAI-compatible (opencode-go) |
| `gemini` | Gemini 3.8 Flash (High) | OpenAI-compatible (Google) |

Each seat gets the same brief (`internal/agent/brief_v2.txt`) and one
packet per book. The packet is built by code from the sealed package only:
the open proposals, Calibre's record, the files' metadata, and the source
records that answered. It carries no file text, OCR or covers, and it sits
in a block marked untrusted, as one line of JSON, fenced by markers with a
fresh random nonce per request (the build fails if the data carries a
marker). The brief is prompt version `v2`; verdicts stored under `v1` are
kept but no longer counted or reused. A judge answers approve,
reject or unsure per field, with a reason. It can't supply a value.

**The rule.**
- All three approve: the proposal is approved by `agent`, through the
  writer's own checks (newest evidence, writable, restorable).
- All three reject: it is rejected by `agent`.
- Anything else (a split, an unsure, a failure, a refused approval) is left
  for a person, with the reason.
- Agents decide only proposals nobody has decided: a person's decision
  always wins, even one made while the panel is judging. A person can
  still change an agent's decision before it is applied.

**Writes.** An agent approval is written by the unchanged Apply, with the
stale check, readback, incidents and undo. After a review that approved
something, bookwarden starts Apply. While writes are off, paused or busy,
it retries every 5 minutes. The "Allow changes to Calibre" switch stays
Felix's.

**Modes.** "Agent fixing" is off (the default), shadow or on.
- In shadow, the panel judges and records, and decides nothing.
- On can only be set after one shadow review has finished and every seat
  has given at least one answer, not just an error, on its run under the
  current prompt version: v1 verdicts are no longer counted, so a v2 shadow
  review is needed.
- Shadow and on both need all three seats' keys.
- A review starts when a check completes in shadow or on, or when someone
  clicks "Review with agents". `bookwarden agent-review --run N
  --calibrate` queues a calibration review, which judges every proposal of
  a run, decided or superseded, and decides nothing.

**Bounds.**
- At most `BOOKWARDEN_AGENT_MAX_WRITES` approvals per review (default
  1,000). Beyond that, proposals are left with "agent ceiling reached".
- At most `BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN` tokens per check, across
  its reviews (default 20,000,000). The cap is checked before a book's
  seats are asked, so the books already in flight (three at most) can
  overshoot it by one call per seat.
- An open incident makes the panel judge without deciding.
- Any error fails closed.

**Records.**
- Verdicts are stored once per review, run, book, field, seat and prompt
  version, with the model, the reason, the tokens and the error code. They
  are never reused across runs.
- A later review asks only the seats that have no answer yet.
- Outcomes record what each review did, and why.
- Neither verdicts nor outcomes are part of the sealed evidence.
- Raw requests and answers are not stored.

**Egress and secrets.**
- The seats' client reaches only the configured hosts, never a private
  address, and doesn't follow redirects.
- Keys come from `BOOKWARDEN_AGENT_ANTHROPIC_KEY`,
  `BOOKWARDEN_AGENT_OPENCODE_KEY` and `BOOKWARDEN_AGENT_GEMINI_KEY` only.
  They are never logged or shown.

## Consequences

- ADR-021's "only after you approve" now reads "after you or the
  unanimous panel approve". Everything else in ADR-021 is unchanged.
- "Never fabricate metadata" holds: a value written by an agent decision
  is the sealed proposal's, made under ADR-020's rules. Agent verdicts are
  a veto layer and never a source of values.
- A fully fooled panel can approve only a value bookwarden already
  proposed. The ceiling bounds one review, and undo reverses it.
- With agent fixing off, bookwarden behaves exactly as before.
- Changing the brief or the packet builder means a new prompt version.
- Agent-picked candidates for the books no field rule fixes (milestone 2)
  need their own ADR: they would relax ADR-020's two-root rule.

## Amendment (2026-11-04): mode on retired

Felix decided (master plan v3) that bookwarden is review-first: nothing is
written to Calibre without his own decision. Mode "on" is retired.

- The modes are off and shadow. `SetMode` refuses "on", and the settings
  page refuses it with a 400. A database whose latest stored mode is "on"
  reads as shadow.
- A review never approves, rejects or applies a proposal. Every outcome is
  "left" for a person, with the panel's result as the reason (for example
  "shadow: approved"). The panel's judging, the verdicts, the outcomes and
  calibration are unchanged.
- Apply never takes an approval made by the agent (`decided_by = 'agent'`),
  so one left from before the retirement is not written; it is not counted
  as waiting either.
- The code that decided and applied for the panel is removed, with the
  write ceiling `BOOKWARDEN_AGENT_MAX_WRITES` (the variable is now ignored),
  the calibration gate for turning on, and the automatic apply.
- The consequence "ADR-021's 'only after you approve' now reads 'after you
  or the unanimous panel approve'" no longer holds: ADR-021 reads as
  written. Agent decisions recorded before this amendment stay in the
  history as they were.
