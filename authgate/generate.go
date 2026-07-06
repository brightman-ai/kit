package authgate

import "crypto/rand"

// authCodeAlphabet is an unambiguous uppercase set (no 0/O, 1/I/L, U) so the printed code is easy
// to read aloud and type. 30 runes → ~4.9 bits each.
const authCodeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// Generate returns a fresh human-friendly auth code: 8 chars grouped 4-4 with a hyphen (e.g.
// "E3X1-M6T2"). crypto/rand + rejection sampling keeps each pick uniform over the alphabet.
//
// Tradeoff: ~39-bit entropy (30^8), far below a 128-bit hex token. That is fine for the local/LAN
// case, and safe enough for a public cloudflare tunnel ONLY because the failure Throttle bounds a
// network brute-force to an infeasible rate. An operator who wants more can set a custom code
// (longer/stronger) via the rotate path — NormalizeCode accepts any letters/digits.
func Generate() string {
	const n = 8
	// Reject bytes in the biased tail so modulo stays uniform over the alphabet.
	limit := byte(256 - (256 % len(authCodeAlphabet)))
	out := make([]byte, 0, n+1)
	buf := make([]byte, 1)
	for i := 0; i < n; i++ {
		if i == 4 {
			out = append(out, '-')
		}
		for {
			rand.Read(buf) //nolint:errcheck
			if buf[0] < limit {
				out = append(out, authCodeAlphabet[int(buf[0])%len(authCodeAlphabet)])
				break
			}
		}
	}
	return string(out)
}
