// Command gen-table regenerates pricing/table_gen.go from LiteLLM's
// model_prices_and_context_window.json — the upstream ccusage prices against.
//
// # WHY A SECOND TABLE
//
// table.go is curated: it carries Anthropic's split cache-write TTLs, the
// long-context premium tiers, and CNY vendors — none of which exist upstream. What
// it cannot be is COMPLETE, because it only ever held the models someone remembered
// to type in. A model nobody typed does not fail loudly; it renders 「—」 and reads
// as a UI bug. So the two tables divide by what each is good at:
//
//	table.go      (hand)      — few entries, rich detail, reviewed. Exact keys WIN.
//	table_gen.go  (generated) — hundreds of entries, flat detail, refreshable.
//
// Neither is a fallback for an UNKNOWN id. An id in no table stays unpriced, and the
// report says so. Breadth is not permission to guess.
//
// # WHY THE NETWORK CODE IS HERE AND NOT IN THE PACKAGE
//
// doc.go forbids network code in the library: a price must be deterministic, offline
// and reviewable in a diff. That rule holds — this is a separate main package run by
// hand, and what lands in the repo is a .go file a human reads in review, never a
// fetch at runtime.
//
// Usage:
//
//	go generate ./pricing/...                                  # refresh from upstream
//	go run ./pricing/internal/gen-table -in snapshot.json       # offline, from a file
//	go run ./pricing/internal/gen-table -date 2026-08-03        # pin the snapshot date
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const upstreamURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// firstPartyProviders is the allowlist of upstream `litellm_provider` values that
// name an actual model VENDOR.
//
// It is short on purpose. Upstream's provider field answers "which endpoint serves
// this", and for most of its ~3000 rows that is a reseller — bedrock, azure,
// fireworks, openrouter, volcengine. A reseller is not a billing vendor: routing
// kimi through volcengine does not make ByteDance the maker of the model, and its
// resale rate is not Moonshot's list price. Letting one through would file money
// under a vendor heading that is simply false.
//
// dashscope (qwen) is deliberately ABSENT even though it is first-party: table.go
// prices qwen in CNY, upstream quotes USD, and mixing them would put two currencies
// under one vendor — which TestOneCurrencyPerVendor exists to forbid, because a
// vendor row that spans currencies has no computable total.
//
// The VALUE is our canonical vendor id, which is not always upstream's provider
// name: upstream says "gemini" (a product) where we say "google" (the biller).
var firstPartyProviders = map[string]string{
	"anthropic": "anthropic",
	"openai":    "openai",
	"gemini":    "google",
	"deepseek":  "deepseek",
	"moonshot":  "moonshot",
	"xai":       "xai",
	"mistral":   "mistral",
	"minimax":   "minimax",
}

// routingPrefixes are region/provider hops at the head of an upstream key. They name
// where a request is routed, not which model it is, and the package's normalize()
// already folds them onto the bare id — so such a key is a duplicate carrying a
// region-adjusted rate we must not publish as the vendor's list price. "ft:" is
// upstream's fine-tune namespace, which is a different product.
var routingPrefixes = []string{"us.", "eu.", "au.", "jp.", "apac.", "global.", "anthropic.", "ft:"}

type upstreamEntry struct {
	Provider            string   `json:"litellm_provider"`
	Mode                string   `json:"mode"`
	InputCostPerToken   *float64 `json:"input_cost_per_token"`
	OutputCostPerToken  *float64 `json:"output_cost_per_token"`
	CacheReadPerToken   *float64 `json:"cache_read_input_token_cost"`
	CacheCreatePerToken *float64 `json:"cache_creation_input_token_cost"`
}

type row struct {
	key, vendor                      string
	in, out, cacheRead, cacheWrite5m float64
}

func main() {
	in := flag.String("in", "", "read upstream JSON from this file instead of fetching")
	out := flag.String("out", "table_gen.go", "path of the generated file")
	date := flag.String("date", "", "snapshot date (YYYY-MM-DD); defaults to today (UTC)")
	flag.Parse()

	raw, err := load(*in)
	if err != nil {
		fail(err)
	}
	var upstream map[string]json.RawMessage
	if err := json.Unmarshal(raw, &upstream); err != nil {
		fail(fmt.Errorf("parse upstream: %w", err))
	}

	rows, kept, skipped := collect(upstream)
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })

	snapshot := *date
	if snapshot == "" {
		snapshot = time.Now().UTC().Format("2006-01-02")
	}
	source := *in
	if source == "" {
		source = upstreamURL
	}
	src, err := format.Source(render(rows, snapshot, source, fmt.Sprintf("%x", sha256.Sum256(raw))))
	if err != nil {
		fail(fmt.Errorf("gofmt: %w", err))
	}
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s: kept %d of %d upstream rows (%d skipped)\n", *out, kept, kept+skipped, skipped)
}

func load(path string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	resp, err := http.Get(upstreamURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", upstreamURL, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// collect turns upstream rows into canonical (model id → rate card) entries.
//
// Every guard below removes a class of silently-wrong money rather than merely
// tidying the output:
//
//   - first-party provider — otherwise the vendor this id resolves to is not the
//     vendor whose rate we copied;
//   - chat/responses mode — embeddings, audio and image models price per unit we do
//     not count, so their per-token rate would be meaningless here;
//   - input AND output present — a half-priced model produces a number that looks
//     complete and is not;
//   - a plain model id — a routing hop is the same model at a different desk.
func collect(upstream map[string]json.RawMessage) (rows []row, kept, skipped int) {
	seen := make(map[string]bool)
	for key, raw := range upstream {
		var e upstreamEntry
		vendor, firstParty := "", false
		if json.Unmarshal(raw, &e) == nil {
			vendor, firstParty = firstPartyProviders[e.Provider]
		}
		if !firstParty || (e.Mode != "chat" && e.Mode != "responses") ||
			e.InputCostPerToken == nil || e.OutputCostPerToken == nil {
			skipped++
			continue
		}
		id := strings.TrimPrefix(key, e.Provider+"/")
		if strings.ContainsAny(id, "/:") || hasAnyPrefix(id, routingPrefixes) || seen[id] {
			skipped++
			continue
		}
		seen[id] = true
		kept++
		rows = append(rows, row{
			key: id, vendor: vendor,
			in: perMillion(e.InputCostPerToken), out: perMillion(e.OutputCostPerToken),
			cacheRead: perMillion(e.CacheReadPerToken), cacheWrite5m: perMillion(e.CacheCreatePerToken),
		})
	}
	return rows, kept, skipped
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// perMillion converts upstream's per-TOKEN rate into this package's per-MILLION unit.
func perMillion(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v * 1e6
}

func render(rows []row, snapshot, source, sum string) []byte {
	var b strings.Builder
	b.WriteString("// Code generated by pricing/internal/gen-table. DO NOT EDIT.\n//\n")
	fmt.Fprintf(&b, "// Source:   %s\n// Snapshot: %s\n// SHA-256:  %s\n//\n", source, snapshot, sum)
	b.WriteString("// Refresh with: go generate ./pricing/...\n\npackage pricing\n\n")
	fmt.Fprintf(&b, `// GeneratedSnapshot is the day this table was pulled from upstream.
//
// It is the VerifiedAt of every price that came from here, in exactly the sense
// RequestQuote.VerifiedAt defines: not when the vendor says the price started, but
// when we last looked. An embedded table cannot notice a price change, so publishing
// its own age is the only defence a reader gets.
const GeneratedSnapshot = %q

// GeneratedSource is where it was pulled from, so a surface showing a generated
// price can name its provenance instead of implying a human read the vendor's page.
const GeneratedSource = %q

`, snapshot, source)
	b.WriteString(`// generatedTable is the upstream snapshot, matched by EXACT id only — never as a
// family fallback. Breadth is its job; guessing is not. An id in neither table stays
// unpriced, which is the answer the report is built to show.
//
// Each row carries its VENDOR as well as its price, because upstream states the
// provider outright and a stated fact beats a prefix rule inferred from the id's
// spelling — "chatgpt-4o-latest" and "o4-mini" are OpenAI's, and no reading of the
// string says so. vendorTable in vendor.go stays for the ids upstream has never
// carried (k3 being the one that started all this).
//
// Upstream publishes ONE cache-write rate, so CacheWrite1h stays 0 and falls back to
// the 5m rate at cost time. A model whose real 1h tier or long-context premium
// matters belongs in table.go, which wins over anything here.
var generatedTable = []genEntry{
`)
	for _, r := range rows {
		fmt.Fprintf(&b, "\t{%q, %q, ModelPrice{Tier: Tier{%s, %s, %s, %s, 0}, Currency: \"USD\"}},\n",
			r.key, r.vendor, num(r.in), num(r.out), num(r.cacheRead), num(r.cacheWrite5m))
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// num prints a rate without scientific notation, so the diff of a refresh reads as
// prices rather than as float formatting.
func num(v float64) string {
	s := strings.TrimRight(fmt.Sprintf("%.6f", v), "0")
	return strings.TrimSuffix(s, ".")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-table:", err)
	os.Exit(1)
}
