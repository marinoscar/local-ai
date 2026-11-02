package catalog

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes of the catalog area (catalog.md section 7, doctor-codes.md).
//
// They are aliases of the constants of internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh). Nothing else in this package spells a code.
const (
	// CodeUnparseable: a catalog file cannot be parsed or loaded (agent start, Load).
	CodeUnparseable = doctor.CAT001
	// CodeRejected: a new catalog failed validation; the previous one is kept.
	CodeRejected = doctor.CAT002
	// CodeUnknownRef: a profile references an unknown model or content id (also in lint).
	CodeUnknownRef = doctor.CAT004

	// CodeSchema: schema problem (unknown key, missing required field, wrong type, schema_version).
	CodeSchema = doctor.CAT010
	// CodePinning: source.revision, sha256 or size_bytes problem (and --online mismatches).
	CodePinning = doctor.CAT011
	// CodeID: bad id format or duplicate id across models and ark.
	CodeID = doctor.CAT012
	// CodeModelFiles: model file rules (llama block, mmproj, draft, chat_template, piper, whisper).
	CodeModelFiles = doctor.CAT013
	// CodeExtraArgs: forbidden llama.extra_args or invalid llama.env.
	CodeExtraArgs = doctor.CAT014
	// CodeArkItem: ark item rules (kiwix refresh_days, bbox, kit/docs tier 1, kit id).
	CodeArkItem = doctor.CAT015
	// CodeOptionalField: extract, extra_repos, checksum/trust/tofu, url vs files misused.
	CodeOptionalField = doctor.CAT016
	// CodePortalLinks: a portal_links rule is broken.
	CodePortalLinks = doctor.CAT017
	// CodeProfile: a profile rule is broken.
	CodeProfile = doctor.CAT018
	// CodeLayout: CATALOG_VERSION missing or malformed, or the file layout is wrong.
	CodeLayout = doctor.CAT019
)
