// Package usage — profile_credentials.go: WHICH coding-plan subscription does a provider
// profile's base URL speak to?
//
// A claude-switch profile routes Claude Code at a vendor's Anthropic-compatible endpoint
// (ANTHROPIC_BASE_URL) with that vendor's key (ANTHROPIC_AUTH_TOKEN). That pair is all the
// evidence needed to DERIVE a subscription account: the same key the routing uses is the key
// the quota API accepts — both vendors answer a plain read on the same host. Deriving the
// subscription from the profile is what keeps ONE key meaning ONE subscription, instead of
// the user typing it twice into a second store and the two copies drifting (the exact
// defect class the credential store's own history records).
//
// The host/vendor table lives HERE because vendor identity is domain vocabulary: this
// package already owns the quota clients and their canonical endpoints, so it owns the
// answer to "does this host speak a coding plan we can ask?". The HOST owns everything else
// about profiles — where they live, how they are read, which of two sources wins. A vendor
// enters this table exactly when its quota client lands in this package.
package usage

import (
	"net/url"
	"strings"
)

// codingPlanAPIHosts maps an Anthropic-compatible routing host to the vendor whose
// coding-plan quota API lives behind it. Hosts are compared lower-cased, port and path
// stripped: kimi routes at …/coding/ and asks quota at …/coding/v1, zhipu routes at
// /api/anthropic and asks at /api/monitor — path prefixes are versioned surface, not
// identity, so a bare host match is exact enough and deliberately ignores them.
var codingPlanAPIHosts = map[string]string{
	"open.bigmodel.cn": VendorZhipu,
	"api.kimi.com":     VendorMoonshot,
}

// DeriveVendorFromBaseURL returns the vendor whose coding plan is served at raw. ok=false
// for everything that is not a known coding-plan endpoint — empty, unparsable, relative,
// unknown host — because "not derivable" is a presence fact, not an error.
func DeriveVendorFromBaseURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	vendor, ok := codingPlanAPIHosts[strings.ToLower(parsed.Hostname())]
	return vendor, ok
}
