package router

// Helpers of the model views (GET /models/{id}, agent.md section 3.8.7): where a model's files are
// on a content root. The layout is <root>/models/<id>/<file name>, the one the preset generator and
// the presence rule use, so a view cannot disagree with them.

// ModelDir is the directory that holds a model's files on a content root.
func ModelDir(root, id string) string { return modelDir(root, id) }

// ModelFilePath is the absolute path of one catalog file of a model on a content root. A name that
// is not a plain relative path (absolute, or climbing out of the model's directory) is an error.
func ModelFilePath(root, id, name string) (string, error) { return filePath(root, id, name) }

// APIError turns an error of the orchestrator into the *api.Error a route answers with: an *OpError
// keeps its doctor code, status and details, with a message built from the situation in the
// language of the request; the agent shutting down is the 503 of the gate; anything else (the
// caller's own context error, a bug) is returned as it is for the server to classify. It is what
// HTTPOps uses; a route that calls the orchestrator directly (POST /key/rotate) uses it too.
func APIError(lang string, err error) error { return apiError(lang, err) }
