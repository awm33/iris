package inference

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// manifest.schema.json is a byte-for-byte copy of spec/manifest.schema.json;
// the conformance package's TestSchemaCopyInSync guards against drift.
//
//go:embed manifest.schema.json
var ManifestSchemaJSON []byte

var compiledSchema = func() *jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ManifestSchemaJSON))
	if err != nil {
		panic("embedded manifest schema unparsable: " + err.Error())
	}
	if err := compiler.AddResource("manifest.schema.json", doc); err != nil {
		panic(err)
	}
	s, err := compiler.Compile("manifest.schema.json")
	if err != nil {
		panic("embedded manifest schema uncompilable: " + err.Error())
	}
	return s
}()

// ValidateManifestDocument checks a fetched manifest against the JSON Schema
// — the registry gates endpoint health on this, so a manifest that would
// fail every job (empty profiles, negative pricing) is rejected upfront
// instead of parsing loosely and failing at dispatch.
func ValidateManifestDocument(raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	if err := compiledSchema.Validate(inst); err != nil {
		return fmt.Errorf("manifest fails schema: %w", err)
	}
	return nil
}
