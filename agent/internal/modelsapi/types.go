package modelsapi

import "github.com/marinoscar/local-ai/agent/internal/statusapi"

// The wire types of the model and key routes. Their json tags mirror the schemas of the same name in
// agent/api/openapi.yaml (ModelList, Model, DownloadAccepted, DeleteModelResponse, Key, KeyRotated);
// TestTypesMirrorSchemas fails when a field exists on one side only. The Go names carry the Service
// results too, so the cloud executors read the same values the routes send.

// ModelList is the body of GET /models (agent.md section 3.8.7; schema ModelList).
type ModelList struct {
	ModelsMax int                  `json:"models_max"`
	Default   *string              `json:"default"`
	Models    []statusapi.ModelRow `json:"models"`
}

// ModelFile is one file of a model on GET /models/{id}. Path is where the file is on the first root
// that holds a complete copy, and "" when the model is not on any mounted root.
type ModelFile struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`
}

// ModelDetail is the body of GET /models/{id}: a ModelRow with its files and, for a model the router
// has a section for, the section's text. The ModelRow fields are embedded, so the JSON stays flat;
// the schema Model describes both. Preset is "" for a model the router has no section for (not on
// any root, not a llama model, or one that does not fit this node).
type ModelDetail struct {
	statusapi.ModelRow
	Files  []ModelFile `json:"files"`
	Preset string      `json:"preset"`
}

// DownloadAccepted is the 202 body of POST /models/{id}/download (schema DownloadAccepted).
type DownloadAccepted struct {
	ID        string `json:"id"`
	SyncRunID string `json:"sync_run_id"`
	Queued    bool   `json:"queued"`
}

// DeleteResult is the body of DELETE /models/{id} (schema DeleteModelResponse). Kept appears only
// when a root's copy was left in place (keep_last, or no copy there); Warnings only when the router
// reload that follows a delete had a problem.
type DeleteResult struct {
	ID       string                   `json:"id"`
	Deleted  []DeletedCopy            `json:"deleted"`
	Kept     []KeptCopy               `json:"kept,omitempty"`
	OpID     string                   `json:"op_id"`
	Warnings []statusapi.WarningBlock `json:"warnings,omitempty"`
}

// KeyBody is the body of GET /key (agent.md section 3.8.13; schema Key). Key is the only place the
// full llama key leaves the agent.
type KeyBody struct {
	Key         string      `json:"key"`
	Fingerprint string      `json:"fingerprint"`
	BaseURLs    KeyBaseURLs `json:"base_urls"`
	Models      []string    `json:"models"`
	Snippets    KeySnippets `json:"snippets"`
}

// KeyBaseURLs are the OpenAI-compatible base URLs of the node: local always, tailnet and public
// when the node has them.
type KeyBaseURLs struct {
	Local   string  `json:"local"`
	Tailnet *string `json:"tailnet"`
	Public  *string `json:"public"`
}

// KeySnippets are ready-to-paste client configurations. They carry the placeholders $KEY and <key>,
// and a file reference for OpenCode, never the key.
type KeySnippets struct {
	Curl         string          `json:"curl"`
	Env          string          `json:"env"`
	OpenCode     OpenCodeSnippet `json:"opencode"`
	OpenAIPython string          `json:"openai_python"`
}

// OpenCodeSnippet is the provider block for OpenCode's configuration file.
type OpenCodeSnippet struct {
	Provider map[string]OpenCodeProvider `json:"provider"`
}

// OpenCodeProvider is one provider of OpenCodeSnippet.
type OpenCodeProvider struct {
	NPM     string                   `json:"npm"`
	Options OpenCodeOptions          `json:"options"`
	Models  map[string]OpenCodeModel `json:"models"`
}

// OpenCodeOptions are the connection options: the base URL and a reference to the key file.
type OpenCodeOptions struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
}

// OpenCodeModel is one model of an OpenCode provider.
type OpenCodeModel struct {
	Limit OpenCodeLimit `json:"limit"`
}

// OpenCodeLimit are the context and output sizes of a model.
type OpenCodeLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// KeyRotated is the body of POST /key/rotate (agent.md section 3.8.14; schema KeyRotated).
type KeyRotated struct {
	Fingerprint     string   `json:"fingerprint"`
	RotatedAt       string   `json:"rotated_at"`
	CloudSynced     bool     `json:"cloud_synced"`
	RestoredModels  []string `json:"restored_models"`
	ClientsToUpdate []string `json:"clients_to_update"`
	OpID            string   `json:"op_id"`
}
