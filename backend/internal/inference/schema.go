package inference

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"

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

// ValidateParams checks a job's params blob against a manifest's
// params_schema (before-real-keys: a param the model never declared must
// be rejected at the API, not forwarded to a paid endpoint or silently
// dropped). Compilation is per-call — job creation is low-rate and the
// schemas are tiny; cache when that stops being true.
func ValidateParams(paramsSchema, params []byte) error {
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(paramsSchema))
	if err != nil {
		return fmt.Errorf("endpoint params_schema is not valid JSON: %w", err)
	}
	if err := compiler.AddResource("urn:iris:params-schema", doc); err != nil {
		return fmt.Errorf("endpoint params_schema unusable: %w", err)
	}
	s, err := compiler.Compile("urn:iris:params-schema")
	if err != nil {
		return fmt.Errorf("endpoint params_schema uncompilable: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(params))
	if err != nil {
		return fmt.Errorf("params are not valid JSON: %w", err)
	}
	if err := s.Validate(inst); err != nil {
		// The library's multi-line message leads with our internal urn —
		// keep the causes, drop the resource preamble.
		msg := err.Error()
		if i := strings.Index(msg, "\n"); i > 0 && strings.Contains(msg[:i], "urn:iris") {
			msg = strings.TrimSpace(msg[i+1:])
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
