package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type compiledToolSchema struct {
	schema *jsonschema.Schema
	err    error
}

// ToolArgumentValidator caches self-contained schemas and is safe for concurrent use.
// It must not be copied after first use.
type ToolArgumentValidator struct {
	schemas sync.Map
}

// Validate enforces the same contract advertised to the model.
// Do not coerce values: changing a command, path, or file content can change the
// requested operation. The loop returns validation errors as paired tool results
// so the model can correct its call on the next round.
func (v *ToolArgumentValidator) Validate(tool Tool, args json.RawMessage) error {
	raw := string(tool.Schema())
	cached, ok := v.schemas.Load(raw)
	if !ok {
		compiled := compileToolSchema(raw)
		cached, _ = v.schemas.LoadOrStore(raw, compiled)
	}
	compiled := cached.(compiledToolSchema)
	if compiled.err != nil {
		return fmt.Errorf("invalid tool schema: %w", compiled.err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return err
	}
	return compiled.schema.Validate(value)
}

func compileToolSchema(raw string) compiledToolSchema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewBufferString(raw))
	if err != nil {
		return compiledToolSchema{err: err}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	// Tool schemas are self-contained; resolving references must not read
	// server files or perform network I/O. Local $defs references still work.
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	const location = "https://rdev.invalid/tool.schema.json"
	if err := compiler.AddResource(location, doc); err != nil {
		return compiledToolSchema{err: err}
	}
	schema, err := compiler.Compile(location)
	return compiledToolSchema{schema: schema, err: err}
}
