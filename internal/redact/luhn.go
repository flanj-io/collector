// Luhn (mod-10) gate for candidate PANs. This is the ONLY thing that turns a 13–19
// digit run into a PAN hit — the floor is Luhn-gated, never brand/BIN-gated (BIN tables
// differ between libraries and reject real 19-digit and regional cards; Luhn is a fixed
// function, so the TS package and this collector agree forever). Mirrors luhn.ts, whose
// `passesLuhn` delegates to validator's isLuhnNumber; here it is a pure function:
// govalidator has no standalone Luhn helper — its IsCreditCard is brand-gated, which
// the floor deliberately is NOT — and Luhn is small enough to own. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

// luhn reports whether an all-digit string passes the Luhn checksum. Input must be
// digits only (the caller has already stripped separators); anything else — including
// the empty string — is false.
func luhn(digits string) bool {
	if len(digits) == 0 {
		return false
	}
	sum := 0
	dbl := false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		dbl = !dbl
	}
	return sum%10 == 0
}
