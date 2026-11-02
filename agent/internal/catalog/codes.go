package catalog

// Doctor codes of the catalog area (catalog.md section 7, doctor-codes.md).
//
// They live in this one file on purpose: scripts/check-doctor-codes.sh forbids a "LAI-..." literal
// in any other non-test Go file once internal/doctor/codes.go exists. AGENT-11 replaces these
// constants with the ones from internal/doctor; nothing else in this package spells a code.
const (
	// CodeUnparseable: a catalog file cannot be parsed or loaded (agent start, Load).
	CodeUnparseable = "LAI-CAT-001"
	// CodeRejected: a new catalog failed validation; the previous one is kept.
	CodeRejected = "LAI-CAT-002"
	// CodeUnknownRef: a profile references an unknown model or content id (also in lint).
	CodeUnknownRef = "LAI-CAT-004"

	// CodeSchema: schema problem (unknown key, missing required field, wrong type, schema_version).
	CodeSchema = "LAI-CAT-010"
	// CodePinning: source.revision, sha256 or size_bytes problem (and --online mismatches).
	CodePinning = "LAI-CAT-011"
	// CodeID: bad id format or duplicate id across models and ark.
	CodeID = "LAI-CAT-012"
	// CodeModelFiles: model file rules (llama block, mmproj, draft, chat_template, piper, whisper).
	CodeModelFiles = "LAI-CAT-013"
	// CodeExtraArgs: forbidden llama.extra_args or invalid llama.env.
	CodeExtraArgs = "LAI-CAT-014"
	// CodeArkItem: ark item rules (kiwix refresh_days, bbox, kit/docs tier 1, kit id).
	CodeArkItem = "LAI-CAT-015"
	// CodeOptionalField: extract, extra_repos, checksum/trust/tofu, url vs files misused.
	CodeOptionalField = "LAI-CAT-016"
	// CodePortalLinks: a portal_links rule is broken.
	CodePortalLinks = "LAI-CAT-017"
	// CodeProfile: a profile rule is broken.
	CodeProfile = "LAI-CAT-018"
	// CodeLayout: CATALOG_VERSION missing or malformed, or the file layout is wrong.
	CodeLayout = "LAI-CAT-019"
)
