package mcp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
)

// toolsJSON defines every MCP tool: name, title, description, annotations and
// input and output schemas, as tools/list serves them. It is the single source
// of truth for the tool contract and is read by the documentation build to
// generate the tool reference. Extension keywords:
//
//   - x-scope: the token scope the tool requires.
//   - x-document-search: the tool exists only when document search is enabled.
//   - x-default: the value a handler uses for an omitted parameter. It is not
//     the standard default keyword, which the SDK would apply itself.
//
//go:embed schema/tools.json
var toolsJSON []byte

// toolDefinition is one tool in schema/tools.json.
type toolDefinition struct {
	Name           string               `json:"name"`
	Title          string               `json:"title"`
	Description    string               `json:"description"`
	Scope          api.Scope            `json:"x-scope"`
	DocumentSearch bool                 `json:"x-document-search"`
	Annotations    *sdk.ToolAnnotations `json:"annotations"`
	InputSchema    *jsonschema.Schema   `json:"inputSchema"`
	OutputSchema   *jsonschema.Schema   `json:"outputSchema"`
}

var toolDefinitions = mustLoadTools(toolsJSON)

func mustLoadTools(data []byte) map[string]toolDefinition {
	defs, err := loadTools(data)
	if err != nil {
		panic(fmt.Sprintf("loading MCP schema/tools.json: %v", err))
	}
	return defs
}

// loadTools parses and checks tool definitions. Unknown top-level fields and
// unknown schema keywords other than x- extensions are rejected, so a typo
// cannot silently drop a limit.
func loadTools(data []byte) (map[string]toolDefinition, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var list []toolDefinition
	if err := decoder.Decode(&list); err != nil {
		return nil, err
	}
	defs := make(map[string]toolDefinition, len(list))
	for _, def := range list {
		switch {
		case def.Name == "" || def.Title == "" || def.Description == "":
			return nil, fmt.Errorf("tool %q needs a name, title and description", def.Name)
		case def.Scope != api.ScopeRead && def.Scope != api.ScopeWrite && def.Scope != api.ScopeSettings:
			return nil, fmt.Errorf("tool %s has unknown x-scope %q", def.Name, def.Scope)
		case def.Annotations == nil || def.InputSchema == nil || def.OutputSchema == nil:
			return nil, fmt.Errorf("tool %s needs annotations, inputSchema and outputSchema", def.Name)
		}
		if _, ok := defs[def.Name]; ok {
			return nil, fmt.Errorf("tool %s is defined twice", def.Name)
		}
		for _, schema := range []*jsonschema.Schema{def.InputSchema, def.OutputSchema} {
			if err := checkKeywords(schema, def.Name); err != nil {
				return nil, err
			}
		}
		defs[def.Name] = def
	}
	return defs, nil
}

// checkKeywords rejects schema keywords jsonschema-go does not know, apart
// from x- extensions.
func checkKeywords(schema *jsonschema.Schema, path string) error {
	if schema == nil {
		return nil
	}
	for key := range schema.Extra {
		if !strings.HasPrefix(key, "x-") {
			return fmt.Errorf("%s: unknown schema keyword %q", path, key)
		}
	}
	for name, property := range schema.Properties {
		if err := checkKeywords(property, path+"."+name); err != nil {
			return err
		}
	}
	if err := checkKeywords(schema.Items, path+"[]"); err != nil {
		return err
	}
	return checkKeywords(schema.AdditionalProperties, path+"{}")
}

// toolDefault returns the x-default of one integer parameter.
func toolDefault(tool, parameter string) int {
	property := toolDefinitions[tool].InputSchema.Properties[parameter]
	if property == nil {
		panic(fmt.Sprintf("MCP tool %s has no parameter %s", tool, parameter))
	}
	value, ok := property.Extra["x-default"].(float64)
	if !ok {
		panic(fmt.Sprintf("MCP tool %s parameter %s has no numeric x-default", tool, parameter))
	}
	return int(value)
}

// addTool registers the tool defined as name in schema/tools.json with its
// handler. The definition's scope is checked before the handler runs. A
// definition whose schemas do not match the handler's Go types stops startup,
// so the published contract cannot drift from what the handler reads and
// returns.
func addTool[In, Out any](s *Server, server *sdk.Server, name string, handler sdk.ToolHandlerFor[In, Out]) {
	def, ok := toolDefinitions[name]
	if !ok {
		panic(fmt.Sprintf("MCP tool %s is not defined in schema/tools.json", name))
	}
	if err := matchesType[In](def.InputSchema); err != nil {
		panic(fmt.Sprintf("MCP tool %s input schema: %v", name, err))
	}
	if err := matchesType[Out](def.OutputSchema); err != nil {
		panic(fmt.Sprintf("MCP tool %s output schema: %v", name, err))
	}
	s.scopes[name] = def.Scope
	tool := &sdk.Tool{
		Name: def.Name, Title: def.Title, Description: def.Description, Annotations: def.Annotations,
		InputSchema: def.InputSchema, OutputSchema: def.OutputSchema,
	}
	sdk.AddTool(server, tool, func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		if err := s.requireScope(ctx, def.Scope); err != nil {
			var zero Out
			return nil, zero, err
		}
		return handler(ctx, req, in)
	})
}

// matchesType reports how schema differs in shape from the schema inferred
// from T: types, properties, required fields and nesting. Descriptions, limits
// and enums are the schema's own.
func matchesType[T any](schema *jsonschema.Schema) error {
	inferred, err := jsonschema.For[T](nil)
	if err != nil {
		return err
	}
	dropNull(inferred)
	return sameShape(schema, inferred, "")
}

func sameShape(declared, inferred *jsonschema.Schema, path string) error {
	if declared == nil || inferred == nil {
		if declared != inferred {
			return fmt.Errorf("%s: declared and Go schemas differ in presence", pathName(path))
		}
		return nil
	}
	if declared.Type != inferred.Type || !slices.Equal(declared.Types, inferred.Types) {
		return fmt.Errorf("%s: type %q, Go type %q", pathName(path), declared.Type, inferred.Type)
	}
	if !slices.Equal(sorted(declared.Required), sorted(inferred.Required)) {
		return fmt.Errorf("%s: required %v, Go requires %v", pathName(path), declared.Required, inferred.Required)
	}
	names := sorted(slices.Collect(maps.Keys(declared.Properties)))
	if goNames := sorted(slices.Collect(maps.Keys(inferred.Properties))); !slices.Equal(names, goNames) {
		return fmt.Errorf("%s: properties %v, Go fields %v", pathName(path), names, goNames)
	}
	for _, name := range names {
		if err := sameShape(declared.Properties[name], inferred.Properties[name], path+"."+name); err != nil {
			return err
		}
	}
	if err := sameShape(declared.Items, inferred.Items, path+"[]"); err != nil {
		return err
	}
	if closed(declared) != closed(inferred) {
		return fmt.Errorf("%s: additionalProperties differs from the Go type", pathName(path))
	}
	return nil
}

func pathName(path string) string {
	if path == "" {
		return "root"
	}
	return strings.TrimPrefix(path, ".")
}

func sorted(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

// closed reports an object that rejects properties it does not list.
func closed(schema *jsonschema.Schema) bool {
	extra := schema.AdditionalProperties
	return extra != nil && extra.Not != nil && extra.Not.Type == "" && len(extra.Not.Types) == 0
}

// dropNull narrows the nullable types inferred from Go slices and pointers
// to their non-null type: HMD never sends or accepts null.
func dropNull(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	if len(schema.Types) == 2 && slices.Contains(schema.Types, "null") {
		for _, typ := range schema.Types {
			if typ != "null" {
				schema.Type = typ
			}
		}
		schema.Types = nil
	}
	for _, property := range schema.Properties {
		dropNull(property)
	}
	for _, def := range schema.Defs {
		dropNull(def)
	}
	dropNull(schema.Items)
	dropNull(schema.AdditionalProperties)
}
