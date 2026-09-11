package main

// The API reference is GENERATED, one endpoint per page (canvas 19c,
// documentation-site.md §6).
//
// core/api/rest/openapi.yaml is the source of truth for the HTTP surface
// (ENGINEERING rule 19), so these pages are read from it and never written by
// hand. The curl example is likewise ASSEMBLED from the operation rather than
// stored: a hand-written example is a lie with a shelf life.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// methods in the order canvas 19c lists them in a sidebar group.
var methodOrder = []string{"get", "post", "put", "patch", "delete"}

// endpoint is one generated page.
type endpoint struct {
	Method      string // upper case, as it is drawn
	Path        string
	OperationID string
	Summary     string
	Description string
	Tag         string
	URL         string
	// Scope is the token ability the route needs, lifted out of the summary's
	// own parenthetical — the spec writes "(owner, session only)" and
	// "(member+)" there, which is where the panel's own reference reads it from.
	Scope  string
	Params []apiParam
	Body   []apiField
	Errors []apiError
	Curl   string
	Sample string
}

type apiParam struct {
	Name, In, Type, Description string
	Required                    bool
}

type apiField struct {
	Name, Type, Notes string
	Required          bool
}

type apiError struct {
	Status, Meaning string
}

// spec is the slice of OpenAPI this reads. Deliberately partial: the site needs
// what canvas 19c draws, and modelling the rest would be a schema library.
type spec struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Paths      map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas map[string]yaml.Node `yaml:"schemas"`
	} `yaml:"components"`
}

type operation struct {
	OperationID string     `yaml:"operationId"`
	Tags        []string   `yaml:"tags"`
	Summary     string     `yaml:"summary"`
	Description string     `yaml:"description"`
	Parameters  []rawParam `yaml:"parameters"`
	RequestBody struct {
		Content map[string]struct {
			Schema yaml.Node `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]response `yaml:"responses"`
}

type response struct {
	Description string `yaml:"description"`
	Content     map[string]struct {
		Schema yaml.Node `yaml:"schema"`
	} `yaml:"content"`
}

type rawParam struct {
	Name        string `yaml:"name"`
	In          string `yaml:"in"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
	Schema      struct {
		Type string `yaml:"type"`
	} `yaml:"schema"`
}

// schemaNode is the part of a JSON Schema the body table needs.
type schemaNode struct {
	Ref        string                `yaml:"$ref"`
	Type       any                   `yaml:"type"`
	Required   []string              `yaml:"required"`
	Properties map[string]yaml.Node  `yaml:"properties"`
	Items      *yaml.Node            `yaml:"items"`
	Enum       []any                 `yaml:"enum"`
	Format     string                `yaml:"format"`
	Desc       string                `yaml:"description"`
	Example    any                   `yaml:"example"`
	AllOf      []yaml.Node           `yaml:"allOf"`
	Extra      map[string]*yaml.Node `yaml:",inline"`
}

// loadAPI reads the spec and flattens it into pages, in the order the file
// declares its paths — which is the order the API was designed in, and reads
// far better than alphabetical.
func loadAPI(path string) (version string, out []endpoint, err error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a build-time path from a flag
	if err != nil {
		return "", nil, fmt.Errorf("docs-site: reading the OpenAPI spec: %w", err)
	}
	var s spec
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return "", nil, fmt.Errorf("docs-site: parsing the OpenAPI spec: %w", err)
	}
	// A map loses declaration order, so the paths are re-read from the document
	// node to recover it.
	order, err := pathOrder(raw)
	if err != nil {
		return "", nil, err
	}
	for _, p := range order {
		node, ok := s.Paths[p]
		if !ok {
			continue
		}
		var byMethod map[string]yaml.Node
		if err := node.Decode(&byMethod); err != nil {
			continue
		}
		for _, m := range methodOrder {
			opNode, ok := byMethod[m]
			if !ok {
				continue
			}
			var op operation
			if err := opNode.Decode(&op); err != nil {
				continue
			}
			out = append(out, buildEndpoint(&s, p, m, op))
		}
	}
	return s.Info.Version, out, nil
}

// pathOrder recovers the declaration order of the `paths` mapping.
func pathOrder(raw []byte) ([]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("docs-site: reading path order: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "paths" {
			continue
		}
		m := doc.Content[i+1]
		out := make([]string, 0, len(m.Content)/2)
		for j := 0; j+1 < len(m.Content); j += 2 {
			out = append(out, m.Content[j].Value)
		}
		return out, nil
	}
	return nil, nil
}

func buildEndpoint(s *spec, apiPath, method string, op operation) endpoint {
	tag := "general"
	if len(op.Tags) > 0 {
		tag = op.Tags[0]
	}
	e := endpoint{
		Method:      strings.ToUpper(method),
		Path:        apiPath,
		OperationID: op.OperationID,
		Summary:     op.Summary,
		Description: op.Description,
		Tag:         tag,
		Scope:       scopeOf(op.Summary),
	}
	e.URL = "/api/" + slugSafe(tag) + "/" + slugSafe(operationSlug(op, method, apiPath)) + "/"

	for _, p := range op.Parameters {
		e.Params = append(e.Params, apiParam{
			Name: p.Name, In: p.In, Type: p.Schema.Type,
			Required: p.Required, Description: oneLine(p.Description),
		})
	}
	if body, ok := op.RequestBody.Content["application/json"]; ok {
		e.Body = fieldsOf(s, body.Schema, 0)
	}
	for _, status := range sortedStatuses(op.Responses) {
		if strings.HasPrefix(status, "2") {
			continue // the happy path is the rail, not the error list
		}
		e.Errors = append(e.Errors, apiError{Status: status, Meaning: oneLine(op.Responses[status].Description)})
	}
	e.Curl = curlFor(e)
	e.Sample = sampleFor(s, op)
	return e
}

// operationSlug prefers the operationId — it is stable, unique and already
// written for a machine. The method+path fallback exists only for an operation
// that forgot one, which the spec's own lint would catch first.
func operationSlug(op operation, method, apiPath string) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	clean := strings.NewReplacer("/", "-", "{", "", "}", "").Replace(strings.TrimPrefix(apiPath, "/"))
	return method + "-" + clean
}

func slugSafe(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '_', r == '-', r == '/':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// scopeOf lifts the rank out of the summary's own parenthetical. The spec
// writes "Set a channel's desired agent version (owner, session only)", which
// is where the panel's in-panel reference reads it from too — one convention,
// not two.
func scopeOf(summary string) string {
	open := strings.LastIndex(summary, "(")
	close := strings.LastIndex(summary, ")")
	if open < 0 || close < open {
		return ""
	}
	inner := strings.TrimSpace(summary[open+1 : close])
	if len(inner) > 48 || strings.Count(inner, " ") > 5 {
		return ""
	}
	return inner
}

// summaryTitle is the summary with the scope parenthetical removed — the page's
// h1 says what the endpoint does, and the scope is a chip beside the path.
func summaryTitle(e endpoint) string {
	if e.Scope == "" {
		return e.Summary
	}
	if i := strings.LastIndex(e.Summary, "("); i > 0 {
		return strings.TrimSpace(e.Summary[:i])
	}
	return e.Summary
}

func sortedStatuses(m map[string]response) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fieldsOf flattens a request schema one level into the body table canvas 19c
// draws. It follows a $ref into components and merges an allOf, because both
// are how this spec composes a request; it does not recurse into a nested
// object, which would make the table a tree and stop being a table.
func fieldsOf(s *spec, node yaml.Node, depth int) []apiField {
	if depth > 3 {
		return nil
	}
	var sn schemaNode
	if err := node.Decode(&sn); err != nil {
		return nil
	}
	if sn.Ref != "" {
		target, ok := resolveRef(s, sn.Ref)
		if !ok {
			return nil
		}
		return fieldsOf(s, target, depth+1)
	}
	var out []apiField
	for _, part := range sn.AllOf {
		out = append(out, fieldsOf(s, part, depth+1)...)
	}
	required := map[string]bool{}
	for _, r := range sn.Required {
		required[r] = true
	}
	for _, name := range propertyOrder(node) {
		prop, ok := sn.Properties[name]
		if !ok {
			continue
		}
		var ps schemaNode
		if err := prop.Decode(&ps); err != nil {
			continue
		}
		out = append(out, apiField{
			Name:     name,
			Type:     typeName(s, ps),
			Notes:    oneLine(ps.Desc),
			Required: required[name],
		})
	}
	return out
}

// propertyOrder recovers declaration order for the properties mapping, so the
// table reads in the order the API was designed rather than alphabetically.
func propertyOrder(node yaml.Node) []string {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "properties" {
			continue
		}
		m := node.Content[i+1]
		out := make([]string, 0, len(m.Content)/2)
		for j := 0; j+1 < len(m.Content); j += 2 {
			out = append(out, m.Content[j].Value)
		}
		return out
	}
	return nil
}

func resolveRef(s *spec, ref string) (yaml.Node, bool) {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return yaml.Node{}, false
	}
	n, ok := s.Components.Schemas[strings.TrimPrefix(ref, prefix)]
	return n, ok
}

func typeName(s *spec, sn schemaNode) string {
	if sn.Ref != "" {
		return strings.TrimPrefix(sn.Ref, "#/components/schemas/")
	}
	switch t := sn.Type.(type) {
	case string:
		if t == "array" && sn.Items != nil {
			var item schemaNode
			if err := sn.Items.Decode(&item); err == nil {
				return typeName(s, item) + "[]"
			}
			return "array"
		}
		return t
	case []any:
		// The spec writes nullable fields as `type: [string, "null"]`.
		parts := make([]string, 0, len(t))
		for _, v := range t {
			parts = append(parts, fmt.Sprint(v))
		}
		return strings.Join(parts, " | ")
	}
	if len(sn.Enum) > 0 {
		return "enum"
	}
	return "object"
}

// curlFor assembles the request example from the operation. Path parameters are
// filled with their own names in braces, which is honest — a fabricated id
// invites copying it.
func curlFor(e endpoint) string {
	var b strings.Builder
	b.WriteString("curl -X " + e.Method + " \\\n")
	b.WriteString("  https://panel.example.dev" + e.Path + " \\\n")
	b.WriteString("  -H \"Authorization: Bearer $CP_TOKEN\"")
	if len(e.Body) > 0 {
		fields := make([]string, 0, 2)
		for _, f := range e.Body {
			if !f.Required && len(fields) > 0 {
				continue
			}
			fields = append(fields, fmt.Sprintf("%q: %s", f.Name, exampleValue(f.Type)))
			if len(fields) == 2 {
				break
			}
		}
		if len(fields) > 0 {
			b.WriteString(" \\\n  -d '{" + strings.Join(fields, ", ") + "}'")
		}
	}
	return b.String()
}

func exampleValue(t string) string {
	switch {
	case strings.HasPrefix(t, "boolean"):
		return "true"
	case strings.HasPrefix(t, "integer"), strings.HasPrefix(t, "number"):
		return "1"
	case strings.HasSuffix(t, "[]"):
		return "[]"
	case strings.HasPrefix(t, "object"):
		return "{}"
	default:
		return `"…"`
	}
}

// sampleFor renders the success response as a JSON skeleton derived from the
// declared schema — the field names the caller will actually receive, with a
// value shaped by each field's type and its own `example` where the spec gives
// one.
//
// Where the operation declares no JSON body it returns "" and the rail omits
// the block entirely. That is the rule §6 states and it matters: a made-up
// response is worse than no response for exactly the reader who copies it.
func sampleFor(s *spec, op operation) string {
	for _, status := range sortedStatuses(op.Responses) {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		body, ok := op.Responses[status].Content["application/json"]
		if !ok {
			continue
		}
		return jsonSkeleton(s, body.Schema, 0)
	}
	return ""
}

// jsonSkeleton renders one schema as pretty JSON. It descends one level into a
// nested object and stops — deeper than that the rail stops being a glance and
// becomes the schema itself, which is what the OpenAPI download is for.
func jsonSkeleton(s *spec, node yaml.Node, depth int) string {
	if depth > 2 {
		return `"…"`
	}
	var sn schemaNode
	if err := node.Decode(&sn); err != nil {
		return ""
	}
	if sn.Ref != "" {
		target, ok := resolveRef(s, sn.Ref)
		if !ok {
			return ""
		}
		return jsonSkeleton(s, target, depth+1)
	}
	if sn.Example != nil {
		return fmt.Sprintf("%q", fmt.Sprint(sn.Example))
	}
	if t, ok := sn.Type.(string); ok && t == "array" && sn.Items != nil {
		inner := jsonSkeleton(s, *sn.Items, depth+1)
		if inner == "" {
			return "[]"
		}
		return "[\n" + indent(inner, depth+1) + "\n" + pad(depth) + "]"
	}
	names := propertyOrder(node)
	if len(names) == 0 {
		for _, part := range sn.AllOf {
			if out := jsonSkeleton(s, part, depth+1); out != "" {
				return out
			}
		}
		return scalarExample(s, sn)
	}
	const maxFields = 6
	var lines []string
	for _, name := range names {
		prop, ok := sn.Properties[name]
		if !ok {
			continue
		}
		var ps schemaNode
		if err := prop.Decode(&ps); err != nil {
			continue
		}
		var value string
		if len(ps.Properties) > 0 || ps.Ref != "" {
			value = jsonSkeleton(s, prop, depth+1)
			if value == "" {
				value = "{}"
			}
		} else {
			value = scalarExample(s, ps)
		}
		lines = append(lines, fmt.Sprintf("%s%q: %s", pad(depth+1), name, value))
		if len(lines) == maxFields {
			lines = append(lines, pad(depth+1)+"…")
			break
		}
	}
	if len(lines) == 0 {
		return "{}"
	}
	return "{\n" + strings.Join(lines, ",\n") + "\n" + pad(depth) + "}"
}

func scalarExample(s *spec, sn schemaNode) string {
	if sn.Example != nil {
		switch v := sn.Example.(type) {
		case string:
			return fmt.Sprintf("%q", v)
		case bool, int, int64, float64:
			return fmt.Sprint(v)
		}
	}
	if len(sn.Enum) > 0 {
		return fmt.Sprintf("%q", fmt.Sprint(sn.Enum[0]))
	}
	switch typeName(s, sn) {
	case "boolean":
		return "false"
	case "integer", "number":
		return "0"
	case "object":
		return "{}"
	}
	if strings.HasSuffix(typeName(s, sn), "[]") {
		return "[]"
	}
	if sn.Format == "date-time" {
		return `"2026-09-07T12:00:00Z"`
	}
	return `"…"`
}

func pad(depth int) string { return strings.Repeat("  ", depth) }
func indent(s string, d int) string {
	return pad(d) + strings.ReplaceAll(s, "\n", "\n"+pad(d))
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
