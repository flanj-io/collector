// The mandatory floor's recognizer set, in APPLICATION order. Each recognizer LOCATES
// candidates with our own code and lets a hardened validator DECIDE (Luhn, mod-97,
// govalidator's email/SSN/IP grammars, phonenumbers' metadata); nothing here is
// decided by regex alone. Mirrors recognizers/index.ts. Contract =
// contracts/redaction-vectors.json + contracts/redaction-fixtures.json.
package redact

// recognizer is the small adapter every built-in recognizer uses: an id and a pure
// find function.
type recognizer struct {
	id   string
	find func(value string, ctx Context) []Span
}

func (r recognizer) ID() string                            { return r.id }
func (r recognizer) Find(value string, ctx Context) []Span { return r.find(value, ctx) }

// Built-in recognizers (exported so callers can compose a custom set with
// WithRecognizers, and so the tests can exercise each one in isolation).
var (
	PANRecognizer   Recognizer = recognizer{id: PAN, find: findPAN}
	EmailRecognizer Recognizer = recognizer{id: EMAIL, find: findEmail}
	IBANRecognizer  Recognizer = recognizer{id: IBAN, find: findIBAN}
	SSNRecognizer   Recognizer = recognizer{id: SSN, find: findSSN}
	PhoneRecognizer Recognizer = recognizer{id: PHONE, find: findPhone}
	CVVRecognizer   Recognizer = recognizer{id: CVV, find: findCVV}
	TokenRecognizer Recognizer = recognizer{id: TOKEN, find: findToken}
	// IPRecognizer is OPTIONAL — off by default; enable with WithIP.
	IPRecognizer Recognizer = recognizer{id: IP, find: findIP}
)

// DefaultRecognizers returns the mandatory floor, in APPLICATION order. Earlier
// recognizers consume structure that would otherwise confuse later ones:
//   - TOKEN and IBAN take their digit runs before the PAN chain scan sees them;
//   - PHONE runs before PAN: the phone locator is `+`-anchored so it can never eat a
//     PAN, but the PAN chain scan CAN eat a phone's national part plus trailing digits
//     when they happen to pass Luhn (`+1 415 555 2671 1225`), so phone must claim its
//     span first.
//
// Reporting order is separate (see ReportOrder). The TS package applies the identical
// order. A fresh slice is returned on every call so callers cannot mutate the floor.
func DefaultRecognizers() []Recognizer {
	return []Recognizer{
		TokenRecognizer,
		CVVRecognizer,
		IBANRecognizer,
		PhoneRecognizer,
		PANRecognizer,
		EmailRecognizer,
		SSNRecognizer,
	}
}
