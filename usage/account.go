// Package usage — account.go: WHOSE quota is this?
//
// The package used to key everything by RUNTIME (claude / codex / gemini), which quietly
// assumed one CLI means one bill. It does not. Point codex at a local translating proxy and
// the traffic is billed to Kimi, not to OpenAI — same CLI, same transcripts, same model
// names in the rollout, entirely different subscription. Keying by runtime made three
// separate defects inevitable:
//
//   - the offline reader scanned the N newest rollouts for an account reading, and a week of
//     proxied sessions (which report no rate limits at all) pushed every real reading out of
//     the window — the account row went blank while the data sat on disk;
//   - a second subscription had nowhere to live, because the registry had no key for it;
//   - "Codex 官方" could not say that the traffic you are producing right now is not being
//     billed to it.
//
// So identity is a PAIR. Runtime says which CLI produced the traffic; Vendor says whose
// subscription paid for it. They are orthogonal, and every reading, snapshot and probe result
// is addressed by both.
package usage

import (
	"sync"

	"github.com/brightman-ai/kit/pricing"
)

// Vendors — the party that owns the subscription and issues the bill.
//
// The ids are PRICING's, not ours. The money axis already keys every row by pricing.Vendor, and
// the subscription tab is about to be decided by joining the two ("does this row's vendor have a
// subscription here?"). A second vocabulary would make that join silently empty — a subscription
// filed under "kimi" could never meet an invoice filed under "moonshot". One table, one truth.
var (
	VendorOpenAI    = pricing.VendorOpenAI.ID    // "openai"
	VendorAnthropic = pricing.VendorAnthropic.ID // "anthropic"
	VendorGoogle    = pricing.VendorGoogle.ID    // "google"
	VendorMoonshot  = pricing.VendorMoonshot.ID  // "moonshot" — 发票名 Kimi
	VendorZhipu     = pricing.VendorZhipu.ID     // "zhipu"    — 智谱 GLM
)

// Account identifies whose quota a reading describes.
type Account struct {
	// Runtime is the CLI that produced the traffic: "claude" | "codex" | "gemini".
	Runtime string `json:"runtime"`
	// Vendor is the party billed: VendorOpenAI | VendorAnthropic | VendorKimi | …
	Vendor string `json:"vendor"`
}

// ID is the stable key for one account — used for snapshot filenames, registry lookups and
// as the frontend's list key. Two accounts are the same account iff their IDs match.
func (a Account) ID() string { return a.Runtime + ":" + a.Vendor }

// subscriptionDisplay overrides the invoice name with the SUBSCRIPTION PRODUCT's name where the
// two differ: the bill says "Kimi", but what you bought is「Kimi For Coding」, and in a panel about
// subscriptions the product is what you recognise. Anything not listed falls back to pricing's
// vendor name, so a newly-priced vendor gets a correct label with no edit here.
var subscriptionDisplay = map[string]string{
	VendorOpenAI:    "Codex 官方",
	VendorAnthropic: "Claude 官方",
	VendorGoogle:    "Gemini 官方",
	VendorMoonshot:  "Kimi For Coding",
	VendorZhipu:     "GLM Coding Plan",
}

// Display names the account for a human.
func (a Account) Display() string {
	if name, ok := subscriptionDisplay[a.Vendor]; ok {
		return name
	}
	if v, ok := pricing.VendorByID(a.Vendor); ok {
		return v.Display // 智谱 GLM / DeepSeek / … 自带正确中文名，无需在此登记
	}
	if a.Vendor != "" {
		return a.Vendor // 没见过的 vendor 就显示它自己的 id —— 那仍是信息，「其他」不是
	}
	return a.Runtime
}

// ── credentials ──────────────────────────────────────────────────────────────
//
// Some vendors answer only when asked with a key (Kimi). kit/usage must not know HOW the host
// stores that key — the terminal seals it with a machine key, another embedder might use a
// keyring or an env var — so the host injects a source and this package only ever asks for a
// vendor by name. The key is used for exactly one outbound call and is never logged, never
// persisted here, and never returned to any caller.

// Credential is what a vendor's quota API needs.
type Credential struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url,omitempty"`
	// RuntimeProviderIDs lists the runtime-side provider ids whose traffic is billed to this
	// vendor — for codex, the values that appear as `session_meta.model_provider` when a local
	// proxy is in use (e.g. "mimo2codex-kimi-coding").
	//
	// It exists so the UI can say WHICH account is being billed right now, and it is DATA, not
	// code: proxy ids are user-chosen strings, so hardcoding one would be a guess that rots.
	// Absent ⟹ we simply do not claim to know. Never inferred.
	RuntimeProviderIDs []string `json:"runtime_provider_ids,omitempty"`
}

// CredentialSource is the host's key store, injected by UseCredentials.
type CredentialSource interface {
	// Credential returns the credential for a vendor, or ok=false when the host holds none.
	Credential(vendor string) (Credential, bool)
}

var (
	credentialsMu  sync.RWMutex
	credentialsSrc CredentialSource
)

// UseCredentials installs the host's key store. Calling it with nil clears the store, which is
// how a host disables the vendors that need a key.
func UseCredentials(src CredentialSource) {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	credentialsSrc = src
}

// credentialFor resolves one vendor's credential. ok=false when no source is installed or the
// source holds nothing for this vendor — both mean "this account is not configured on this
// host", which is a presence fact, not an error.
func credentialFor(vendor string) (Credential, bool) {
	credentialsMu.RLock()
	src := credentialsSrc
	credentialsMu.RUnlock()
	if src == nil {
		return Credential{}, false
	}
	cred, ok := src.Credential(vendor)
	if !ok || cred.APIKey == "" {
		return Credential{}, false
	}
	return cred, true
}

// vendorForRuntimeProvider maps a runtime-side provider id onto the vendor it bills, using only
// what the host declared (see Credential.RuntimeProviderIDs). The empty string means "we do not
// know", which the caller must render as unknown rather than as any particular vendor.
func vendorForRuntimeProvider(providerID string) string {
	if providerID == "" {
		return ""
	}
	for _, vendor := range attributionVendors {
		cred, ok := credentialFor(vendor)
		if !ok {
			continue
		}
		for _, id := range cred.RuntimeProviderIDs {
			if id == providerID {
				return vendor
			}
		}
	}
	return ""
}
