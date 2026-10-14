package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A codex signed in to ChatGPT under a custom provider id is the same subscription, and its
// rollouts carry the account's own rate_limits. Observed 2026-10-13: model_provider =
// "local_codex", twelve rollouts in a day with 5% → 13% weekly usage, and the panel frozen at a
// morning probe — because the scan only accepted the literal "openai", and the host's keyless
// declaration of "local_codex" never reached the mapping (credentialFor demanded a key).

// aliasRollout writes a rollout whose session_meta names `provider` and whose rate_limits are an
// account reading at usedPct on the weekly window.
func (e *quotaEnv) aliasRollout(t *testing.T, provider string, usedPct float64) {
	t.Helper()
	dir := filepath.Join(e.codexHome, "sessions", "2026", "10", "13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)
	reset := time.Now().Add(6 * 24 * time.Hour).Unix()
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":"s1","model_provider":%q}}`, at, provider)
	reading := fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","limit_name":null,"plan_type":"pro","primary":{"used_percent":4,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":%v,"window_minutes":10080,"resets_at":%d}}}}`,
		at, reset, usedPct, reset)
	write(t, filepath.Join(dir, "rollout-2026-10-13T12-00-00-alias.jsonl"), meta+"\n"+reading+"\n")
}

func TestCodexRollout_DeclaredOfficialAliasIsTheOwnAccount(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.aliasRollout(t, "local_codex", 13)
	// The host's declaration: keyless — the account is reached through codex's own login — and it
	// states ownership, not just billing: the id is in OwnAccountProviderIDs.
	withCredentials(t, fakeCredentials{VendorOpenAI: {
		RuntimeProviderIDs:    []string{"local_codex"},
		OwnAccountProviderIDs: []string{"local_codex"},
	}})

	readings := codexRolloutScan()
	if len(readings) == 0 {
		t.Fatal("a rollout under a DECLARED alias of the own account was discarded as somebody else's bill")
	}
}

// F4: billed to OpenAI is not the same as running on the user's OpenAI account. A relay declared
// as OpenAI (RuntimeProviderIDs) bills OpenAI yet may pass ANOTHER account's rate_limits through;
// reading them would overwrite the user's own quota with a stranger's.
func TestCodexRollout_VendorMappingAloneIsNotOwnership(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.aliasRollout(t, "openai-relay", 77)
	withCredentials(t, fakeCredentials{VendorOpenAI: {RuntimeProviderIDs: []string{"openai-relay"}}})

	if readings := codexRolloutScan(); len(readings) != 0 {
		t.Fatalf("a provider declared as OpenAI's BILLING vendor was read as the user's own account: "+
			"%d readings — a relay's rate_limits are its owner's", len(readings))
	}
}

func TestCodexProviderBillsOwnAccount(t *testing.T) {
	withCredentials(t, fakeCredentials{
		VendorOpenAI:   {RuntimeProviderIDs: []string{"relay", "official-alias"}, OwnAccountProviderIDs: []string{"official-alias"}},
		VendorMoonshot: {OwnAccountProviderIDs: []string{"kimi-own"}}, // ownership is read from the OpenAI credential only
	})
	for provider, want := range map[string]bool{
		"":               true,  // older rollouts predate session_meta.model_provider
		"openai":         true,  // codex's own provider
		"official-alias": true,  // declared as running on the OpenAI account itself
		"relay":          false, // billed to OpenAI by mapping, but not shown to be the own account
		"kimi-own":       false, // owned by some OTHER credential
		"stranger":       false,
	} {
		if got := codexProviderBillsOwnAccount(provider); got != want {
			t.Errorf("codexProviderBillsOwnAccount(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestCodexProviderBillsOwnAccount_NoSource(t *testing.T) {
	withCredentials(t, fakeCredentials{})
	if !codexProviderBillsOwnAccount("") || !codexProviderBillsOwnAccount("openai") {
		t.Fatal("codex's own provider needs no declaration")
	}
	if codexProviderBillsOwnAccount("local_codex") {
		t.Fatal("an undeclared name is not evidence")
	}
}

func TestCodexRollout_UndeclaredAliasStaysSomebodyElses(t *testing.T) {
	env := newQuotaEnv(t).withCodexAuth(t, false).withCLI(t, "codex")
	env.aliasRollout(t, "local_codex", 13)
	withCredentials(t, fakeCredentials{}) // nobody said whose "local_codex" is

	if readings := codexRolloutScan(); len(readings) != 0 {
		t.Fatalf("an undeclared provider name must not be read as the own account: a relay can pass "+
			"its owner's rate_limits through; got %d readings", len(readings))
	}
}

func TestDeclaration_KeylessMappingMapsButDoesNotProbe(t *testing.T) {
	withCredentials(t, fakeCredentials{VendorOpenAI: {RuntimeProviderIDs: []string{"local_codex"}}})
	if got := vendorForRuntimeProvider("local_codex"); got != VendorOpenAI {
		t.Fatalf("keyless declaration did not map: got %q", got)
	}
	if _, ok := credentialFor(VendorOpenAI); ok {
		t.Fatal("a keyless declaration must not pass for a credential a probe can authenticate with")
	}
	if AttributionRevision() == "none" {
		t.Fatal("the attribution fingerprint must cover keyless mappings, or a new declaration would not invalidate priced days")
	}
}

func TestProbeFailure_ReasonSurvivesBeingRecorded(t *testing.T) {
	t.Setenv("DEEPWORK_HOME", t.TempDir())
	a := Account{Runtime: "codex", Vendor: VendorOpenAI}
	now := time.Now()
	recordProbeFailure(a, now, "read tcp: connection reset by peer")
	at, msg := lastProbeFailure(a, time.Hour)
	if msg != "read tcp: connection reset by peer" || at.Unix() != now.Unix() {
		t.Fatalf("recorded failure read back as (%v, %q) — the reason was lost on its own write", at, msg)
	}
}
