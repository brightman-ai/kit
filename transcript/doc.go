// Package transcript is the cross-repo SSOT for the agent-session transcript
// domain: one domain model (Transcript/Turn/Block), one Source port with an ACL
// adapter per runtime (claude jsonl / codex jsonl / deepwork-native jsonl), one
// aggregator, and the touched-files domain service (ExtractTouched).
//
// Terminal abstraction (SSOT-SESSION-LOADER §6): the agent runtime OWNS its
// transcript; this package is a read-only viewer/normalizer. Adding a runtime =
// adding a Source adapter; the domain model, aggregator, touched service and every
// downstream consumer are unchanged. Soft-delete/rename live in the host's own
// HiddenStore overlay and never touch the runtime transcript (SSOT protection).
//
// Consumers: deepwork-pro (webui read side + native write side) and
// deepwork-terminal both import this package instead of re-declaring the model +
// parsers — collapsing three hand-synced copies into one. Pure stdlib; the DB
// coupling is inverted via the DeepworkSessionProvider / HiddenStore interfaces.
package transcript
