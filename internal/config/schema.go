package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
)

// SchemaID is the canonical $id of the generated config schema.
const SchemaID = "https://github.com/abs/mysql-mcp/raw/main/schema/config.schema.json"

// decodeStrict unmarshals config JSON, rejecting unknown fields so typos in the
// config surface as errors rather than being silently ignored.
func decodeStrict(raw []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GenerateSchema reflects the Config type into a JSON Schema document. Field
// descriptions are taken from Go doc comments when the source tree is available
// (i.e. when run from a repository checkout), and omitted otherwise.
func GenerateSchema() ([]byte, error) {
	r := &jsonschema.Reflector{
		// Keep additionalProperties:false so the schema rejects typos, matching
		// the strict decoder.
		RequiredFromJSONSchemaTags: false,
	}
	// Best effort: enrich the schema with doc comments from the source tree.
	_ = r.AddGoComments("github.com/abs/mysql-mcp", "./internal/config")

	schema := r.Reflect(&Config{})
	schema.ID = jsonschema.ID(SchemaID)
	schema.Title = "mysql-mcp configuration"

	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return append(out, '\n'), nil
}
