package flanjui

// User-facing relay messages (the UX copy deck, v0.1a). Every error the relay
// returns to the local UI is `{ "error": "<code>", "message": "<human>" }`; the
// codes are stable identifiers the UI switches on, the messages are the deck's
// strings. Naming rule: no "peek / magic link / minting / invite / invitee /
// previewer" in anything user-facing — the naming denylist test scans this file
// together with the UI sources.
const (
	msgNotConnected = "Connect first. Creating a thread link needs your org name and a confirmed contact email — viewing local data never does."
	// msgThreadsNotConnected is the LIST answer, and it deliberately does not say
	// "no threads": the threads live on the control plane, so a collector without
	// a key cannot tell an empty list from a list it can't read.
	//
	// The UI no longer PROVOKES this 412: a refused request is logged by the
	// browser as a failed resource on every poll tick, so the SPA gates the poll
	// on the connect state it already holds and mirrors this string as
	// THREADS_NOT_CONNECTED_NOTICE in ui/src/threads.ts. The route keeps
	// answering 412 (the contract, and any other client), but change this line
	// and the mirror changes with it.
	msgThreadsNotConnected = "Not connected — this collector can't list threads. Connect in Settings to see them."
	msgContactUnconfirmed  = "Confirm your contact email first — we sent \"Confirm your Flanj contact\"."
	msgCPUnreachableFlag   = "Couldn't reach the control plane — nothing was created or shared."
	msgCPUnreachableSend   = "Couldn't reach the control plane — nothing was sent."
	msgCPUnreachable       = "Couldn't reach the control plane."
	msgCPNotConfigured     = "The control plane is not configured on this collector (set cp_base_url and cp_deploy_token)."
	msgStoreUnavailable    = "The local store is not available."
	msgPostOnly            = "POST only."
	msgUIHeaderRequired    = "This action is only available from the collector UI."
	msgJSONRequired        = "Send a JSON body (Content-Type: application/json)."
	msgForeignOrigin       = "This action is only available from the collector's own page."
	msgInvalidJSON         = "The request body is not valid JSON."
	msgConnectFields       = "Your organization and a contact email are required."
	msgInvalidEmail        = "Enter a valid email."
	msgFindingRequired     = "finding_id is required."

	// Contract upload. Uploaded contracts stay on this collector — no string
	// here may suggest otherwise, and no code path on that route reaches the
	// control plane.
	msgContractHostRequired        = "Which provider is this contract for? Enter its host, e.g. api.acme.test."
	msgContractHostInvalid         = "That doesn't look like a host. Enter just the domain, e.g. api.acme.test."
	msgContractHostTooLong         = "That host is too long to be a domain name."
	msgContractHostPunycode        = "Enter the host as it appears in the traffic (ASCII, punycode for international domains)."
	msgContractHostBadPort         = "That port isn't a number between 1 and 65535. Enter the host as the traffic carries it, e.g. api.acme.test:8080."
	msgContractDocumentRequired    = "Choose an OpenAPI document to upload."
	msgContractTooLarge            = "That document is larger than 8 MB. Contracts this size are usually a bundle — upload the API's own document."
	msgContractStoreFailed         = "Couldn't save the contract to the local store."
	msgContractIntegrationRequired = "integration is required."
	msgContractNotRemovable        = "This contract comes from the collector's config file, not an upload — remove it there."
	msgContractNotLoaded           = "No contract is loaded for that integration."
	msgNotFlaggable        = "This finding is a local notice — stale-client calls stay on this collector and can't be flagged to the provider."
	msgNotAckable          = "Only non-breaking informational findings can be acknowledged — breaking findings need a fix or a thread."
	msgFindingNotFound     = "That finding is no longer in the local store."
	msgFindingNoCall       = "This finding has no failing call to share (spec-version findings are informational)."
	msgCallEvicted         = "The failing call is no longer in the local store (it was evicted from the rolling window)."
	msgThreadNotFound      = "No thread with that id was created from this collector."
	msgWrongOrigin         = "This thread was created by another collector key — it can only be changed from there."
	msgKeyMissing          = "The control plane already knows this collector, but this store never received its key. Set a new cp_deploy_token and Connect again."
	// Edge naming (v1 phase 1).
	msgEdgeHostRequired = "host is required."
	msgEdgeNotFound     = "No outbound edge with that host has been discovered."
	msgNameTooLong      = "The name is too long."
	msgNameEmpty        = "The name is empty."
	// The distinct partial-success copy: the local save landed, only the
	// OPT-IN directory suggestion did not go out (brief-common copy deck).
	msgNameSavedSuggestFailed = "Name saved. The suggestion didn't reach the directory — it stays local."
)

// msgNameSuggestRefused is the OTHER partial-success copy: the local save
// landed, and the directory REFUSED the suggestion (a CP 400 from the name
// normalizer — not a transport failure), relaying the CP's one-sentence reason.
// The wrapper deliberately does NOT say "refused": the CP's own sentence already
// carries the refusal ("The name … doesn't accept."), and doubling it read as two
// rejections stacked on one line.
func msgNameSuggestRefused(cpMessage string) string {
	return "Name saved. The directory didn't take the suggestion: " + cpMessage
}
