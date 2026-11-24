// Package agent is agent fixing (ADR-029): a panel of three LLM judges from
// three vendors reviews a run's open proposals from the sealed evidence. What
// all three approve is approved, what all three reject is rejected, and the
// rest is left for a person. Judges only choose; values come from the sealed
// proposals.
package agent

import _ "embed"

// PromptVersion names the brief and the packet builder together. Changing
// either means a new version, so verdicts are never mixed across them.
const PromptVersion = "v2"

//go:embed brief_v2.txt
var briefV2 string

// Brief is the judging brief every seat gets as its system prompt.
func Brief() string { return briefV2 }
