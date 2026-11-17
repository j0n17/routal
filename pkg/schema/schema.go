// Package schema holds the JSON schema of router.yaml, for editors (yaml-language-server:
// completion, field documentation, errors while typing). It is generated from the types of
// pkg/config and their comments (make schema); routald's own validation stays the authority: the
// schema knows nothing of references between documents or of values that must agree.
package schema

import _ "embed"

// JSON is the schema (draft-07); every YAML document of router.yaml must match it.
//
//go:embed router.schema.json
var JSON []byte

// URL is where the schema of the main branch is published.
const URL = "https://raw.githubusercontent.com/j0n17/routal/main/pkg/schema/router.schema.json"
