package viniferaui

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
	msgThreadsNotConnected = "Not connected — this collector can't list threads. Connect in Settings to see them."
	msgContactUnconfirmed  = "Confirm your contact email first — we sent \"Confirm your Vinifera contact\"."
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
	msgNotFlaggable        = "This finding is a local notice — stale-client calls stay on this collector and can't be flagged to the provider."
	msgNotAckable          = "Only non-breaking informational findings can be acknowledged — breaking findings need a fix or a thread."
	msgFindingNotFound     = "That finding is no longer in the local store."
	msgFindingNoCall       = "This finding has no failing call to share (spec-version findings are informational)."
	msgCallEvicted         = "The failing call is no longer in the local store (it was evicted from the rolling window)."
	msgThreadNotFound      = "No thread with that id was created from this collector."
	msgWrongOrigin         = "This thread was created by another collector key — it can only be changed from there."
	msgKeyMissing          = "The control plane already knows this collector, but this store never received its key. Set a new cp_deploy_token and Connect again."
)
