package inference

import (
	"encoding/json"
	"strings"
	"testing"
)

func manifestWithParams(t *testing.T, schema string) *Manifest {
	t.Helper()
	raw := `{"spec_version":"1.0","id":"m","family":"f","version":"1","modality":"image",
	  "tasks":["t2i"],"profiles":{"draft":{"max_width":1024,"max_height":1024}},
	  "features":{"prompt":true,"seed":true}` +
		func() string {
			if schema == "" {
				return "}"
			}
			return `,"params_schema":` + schema + `}`
		}()
	m, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidateParams(t *testing.T) {
	schema := `{"type":"object","additionalProperties":false,
	  "properties":{"guidance":{"type":"number","minimum":1,"maximum":20}}}`
	m := manifestWithParams(t, schema)
	base := &CreateJobRequest{Task: "t2i", Profile: "draft", Prompt: "x",
		Output: &Output{Width: 512, Height: 512}}

	ok := *base
	ok.Params = json.RawMessage(`{"guidance": 7}`)
	if err := m.Validate(&ok); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}

	// Out-of-range value
	bad := *base
	bad.Params = json.RawMessage(`{"guidance": 99}`)
	if err := m.Validate(&bad); err == nil || !strings.Contains(err.Error(), "params rejected") {
		t.Fatalf("out-of-range param must fail: %v", err)
	}

	// Undeclared param — the before-real-keys case: must fail at the API,
	// never forwarded to a paid endpoint or silently dropped.
	undeclared := *base
	undeclared.Params = json.RawMessage(`{"guidance": 7, "steps": 500}`)
	if err := m.Validate(&undeclared); err == nil {
		t.Fatal("undeclared param must fail")
	}

	// Wrong type
	wrong := *base
	wrong.Params = json.RawMessage(`{"guidance": "high"}`)
	if err := m.Validate(&wrong); err == nil {
		t.Fatal("wrong-typed param must fail")
	}

	// Params against a model that declares NO schema
	noSchema := manifestWithParams(t, "")
	sneaky := *base
	sneaky.Params = json.RawMessage(`{"anything": 1}`)
	if err := noSchema.Validate(&sneaky); err == nil {
		t.Fatal("params against a schema-less model must fail")
	}

	// Empty/null params always fine
	for _, p := range []string{"", "null", "{}", "{ }", "\n{\n}\n"} {
		empty := *base
		if p != "" {
			empty.Params = json.RawMessage(p)
		}
		if err := noSchema.Validate(&empty); err != nil {
			t.Fatalf("empty params %q must pass: %v", p, err)
		}
	}
}
