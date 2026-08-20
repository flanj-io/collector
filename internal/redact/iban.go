// IBAN validation: ISO 13616 mod-97 checksum plus the per-country length registry.
// The TS mirror (recognizers/iban.ts) hands its candidates to validator's isIBAN;
// the collector owns the equivalent check so the floor stays free of any network-capable
// dependency for this pattern. The registry below is derived from that validator's
// country table so both languages accept the same countries. Pure, no I/O. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

// ibanLengths is the stripped (separator-free) length per ISO 3166 country code.
// A country missing from the registry is not a valid IBAN (fail-closed, like the TS
// validator, which only knows these countries).
var ibanLengths = map[string]int{
	"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28, "BA": 20, "BE": 16, "BG": 22,
	"BH": 22, "BR": 29, "BY": 28, "CH": 21, "CR": 22, "CY": 28, "CZ": 24, "DE": 22,
	"DK": 18, "DO": 28, "EE": 20, "EG": 29, "ES": 24, "FI": 18, "FO": 18, "FR": 27,
	"GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27, "GT": 28, "HR": 21, "HU": 28,
	"IE": 22, "IL": 23, "IQ": 23, "IR": 26, "IS": 26, "IT": 27, "JO": 30, "KW": 30,
	"KZ": 20, "LB": 28, "LC": 32, "LI": 21, "LT": 20, "LU": 20, "LV": 21, "MC": 27,
	"MD": 24, "ME": 22, "MK": 19, "MR": 27, "MT": 31, "MU": 30, "MZ": 25, "NL": 18,
	"NO": 15, "PK": 24, "PL": 28, "PS": 29, "PT": 25, "QA": 29, "RO": 24, "RS": 22,
	"SA": 24, "SC": 31, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "SV": 28, "TL": 23,
	"TN": 24, "TR": 26, "UA": 29, "VA": 22, "VG": 24, "XK": 20,
}

// isIBAN reports whether s (possibly print-formatted with spaces, possibly lowercase)
// is a valid IBAN: spaces removed, ASCII letters uppercased, all alphanumeric, length
// equal to the registry length for its country, and mod-97 remainder 1 (ISO 7064).
func isIBAN(s string) bool {
	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			continue
		case c >= 'a' && c <= 'z':
			buf = append(buf, c-'a'+'A')
		case (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			buf = append(buf, c)
		default:
			return false
		}
	}
	if len(buf) < 4 {
		return false
	}
	want, ok := ibanLengths[string(buf[:2])]
	if !ok || len(buf) != want {
		return false
	}
	return ibanMod97(buf) == 1
}

// ibanMod97 computes the ISO 7064 mod-97-10 remainder: move the first four characters
// to the end, map A..Z to 10..35, and reduce the resulting digit string modulo 97 in a
// single streaming pass (equivalent to the TS validator's chunked reduction).
func ibanMod97(stripped []byte) int {
	rearranged := make([]byte, 0, len(stripped))
	rearranged = append(rearranged, stripped[4:]...)
	rearranged = append(rearranged, stripped[:4]...)
	n := 0
	for _, c := range rearranged {
		if c >= '0' && c <= '9' {
			n = (n*10 + int(c-'0')) % 97
		} else {
			v := int(c-'A') + 10 // 10..35, always two digits
			n = (n*100 + v) % 97
		}
	}
	return n
}
