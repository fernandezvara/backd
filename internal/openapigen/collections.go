package openapigen

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/strutil"
)

// addFileDetails declares what backd stores about an uploaded file.
func (b *builder) addFileDetails() {
	b.extra["FileDetails"] = map[string]any{
		"type":        "object",
		"description": "What backd stores about an uploaded file. Only uploads write it.",
		"required":    []any{"id", "name", "size", "type", "sha256", "uploaded_at"},
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"size":        map[string]any{"type": "integer"},
			"type":        map[string]any{"type": "string"},
			"sha256":      map[string]any{"type": "string"},
			"uploaded_at": map[string]any{"$ref": "#/components/schemas/Timestamp"},
			"width":       map[string]any{"type": "integer"},
			"height":      map[string]any{"type": "integer"},
			"versions":    map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object"}},
			"url":         map[string]any{"type": "string", "description": "A public file's permanent address, or (with ?file_links=true) a signed link."},
			"expires_at":  map[string]any{"$ref": "#/components/schemas/Timestamp"},
			"public":      map[string]any{"type": "boolean", "description": "True for a file in the realm's public storage."},
			"key_name":    map[string]any{"type": "string", "description": "A public file's name as its address ends with it."},
		},
	}
}

// toSchemas converts a JSON Schema document (draft 2020-12, with local $refs into its own
// $defs) into OpenAPI component schemas under the given name: the root as name, each $def as
// name_<def>. It returns the root's reference.
func (b *builder) toSchemas(name string, raw map[string]any) {
	doc := docutil.DeepCopy(jsonRoundTrip(raw)).(map[string]any)
	names := map[string]string{"#": name}
	for _, k := range []string{"$defs", "definitions"} {
		if defs, ok := doc[k].(map[string]any); ok {
			for d := range defs {
				names["#/"+k+"/"+d] = name + "_" + strutil.Pascal(d)
			}
		}
	}
	var fix func(v any)
	fix = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if ref, ok := t["$ref"].(string); ok {
				if to, ok := names[ref]; ok {
					t["$ref"] = "#/components/schemas/" + to
				}
			}
			for _, e := range t {
				fix(e)
			}
		case []any:
			for _, e := range t {
				fix(e)
			}
		}
	}
	for _, k := range []string{"$defs", "definitions"} {
		if defs, ok := doc[k].(map[string]any); ok {
			for d, s := range defs {
				fix(s)
				b.extra[name+"_"+strutil.Pascal(d)] = s
			}
			delete(doc, k)
		}
	}
	delete(doc, "$schema")
	delete(doc, "$id")
	fix(doc)
	b.extra[name] = doc
}

// jsonRoundTrip turns json.Number and friends into plain JSON values.
func jsonRoundTrip(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(data, &out) != nil {
		return v
	}
	return out
}

// collection adds the collection's paths and schemas to the client API.
func (b *builder) collection(db string, c *registry.Collection) {
	b.collectionAt(db, c, false)
}

// collectionAt adds the collection's schemas and its paths: those clients use, subject to the
// collection's rules, or (admin) the admin data routes, which are past the rules.
func (b *builder) collectionAt(db string, c *registry.Collection, admin bool) {
	alias := strutil.Pascal(db) + strutil.Pascal(c.Name)
	tag := db + "/" + c.Name
	contract := "/v1/{realm}/{database}/{collection}"
	base := "/v1/" + b.realm + "/" + db + "/" + c.Name
	if admin {
		tag = "data " + tag
		contract = "/v1/{realm}/_admin/data/{database}/{collection}"
		base = "/v1/" + b.realm + "/_admin/data/" + db + "/" + c.Name
	}
	allowed := func(op rules.Op) bool { return admin || b.allowed(c, op) }

	// Schemas: the stored document (with id, _meta and file details) and what a client writes.
	b.toSchemas(alias, c.RawSchema)
	props := func(schema map[string]any) map[string]any {
		p, _ := schema["properties"].(map[string]any)
		if p == nil {
			p = map[string]any{}
			schema["properties"] = p
		}
		return p
	}
	doc := b.extra[alias].(map[string]any)
	input := docutil.DeepCopy(doc).(map[string]any)
	for fileField, f := range c.Files {
		if f == nil {
			continue
		}
		ref := map[string]any{"$ref": "#/components/schemas/FileDetails"}
		if f.Multiple {
			props(doc)[fileField] = map[string]any{"type": "array", "items": ref}
		} else {
			props(doc)[fileField] = ref
		}
		delete(props(input), fileField)
		if req, ok := input["required"].([]any); ok {
			var kept []any
			for _, r := range req {
				if r != fileField {
					kept = append(kept, r)
				}
			}
			input["required"] = kept
		}
	}
	props(doc)["id"] = map[string]any{"type": "string"}
	props(doc)["_meta"] = map[string]any{"$ref": "#/components/schemas/Meta"}
	req, _ := doc["required"].([]any)
	doc["required"] = append(req, "id", "_meta")
	if _, ok := doc["type"]; !ok {
		doc["type"] = "object"
	}
	b.extra[alias] = doc
	b.extra[alias+"Input"] = input
	// A collection with custom ids takes the id in a create's body, required.
	createBody := alias + "Input"
	var idSchema map[string]any
	if cid := c.CustomID; cid != nil {
		idSchema = map[string]any{"type": "string", "minLength": 1, "maxLength": registry.MaxIDLength}
		if cid.User {
			idSchema["description"] = "Chosen by the client, and never changes: it must be the id of the signed-in user creating the document."
		} else {
			idSchema["pattern"] = cid.Anchored()
			idSchema["description"] = "Chosen by the client, and never changes. Letters, digits and - . _ ~ : @ only, not starting with _ or ."
		}
		create := docutil.DeepCopy(input).(map[string]any)
		props(create)["id"] = idSchema
		creq, _ := create["required"].([]any)
		create["required"] = append([]any{"id"}, creq...)
		b.extra[alias+"Create"] = create
		createBody = alias + "Create"
	}
	// Lists: the contract's page with this collection's documents.
	list := docutil.DeepCopy(b.components("schemas")["DocumentList"]).(map[string]any)
	rewriteRefs(list, map[string]string{"#/components/schemas/Document": "#/components/schemas/" + alias})
	b.extra[alias+"List"] = list
	// The single-document response of the contract, typed.
	resp := docutil.DeepCopy(b.components("responses")["Document"]).(map[string]any)
	rewriteRefs(resp, map[string]string{"#/components/schemas/Document": "#/components/schemas/" + alias})
	b.responses[alias+"Document"] = resp

	mapping := map[string]string{
		"#/components/schemas/Document":     "#/components/schemas/" + alias,
		"#/components/schemas/DocumentBody": "#/components/schemas/" + alias + "Input",
		"#/components/schemas/DocumentList": "#/components/schemas/" + alias + "List",
		"#/components/responses/Document":   "#/components/responses/" + alias + "Document",
	}
	opNames := map[string]string{
		"listDocuments": "list" + alias, "createDocument": "create" + alias, "getDocument": "get" + alias,
		"replaceDocument": "replace" + alias, "patchDocument": "patch" + alias, "deleteDocument": "delete" + alias,
		"aggregateDocuments": "aggregate" + alias,
	}
	needs := map[string]rules.Op{
		"listDocuments": rules.Read, "getDocument": rules.Read, "aggregateDocuments": rules.Read, "createDocument": rules.Create,
		"replaceDocument": rules.Update, "patchDocument": rules.Update, "deleteDocument": rules.Delete,
	}

	prefix := contract
	sub := func(item map[string]any, path string) {
		cp := docutil.DeepCopy(item).(map[string]any)
		dropParams(cp, "realm", "database", "collection")
		for method, op := range operations(cp) {
			id, _ := op["operationId"].(string)
			if need, ok := needs[id]; ok && !allowed(need) {
				delete(cp, method)
				continue
			}
			if strings.HasSuffix(strings.ToLower(id), "restoredocument") && (c.SoftDelete == nil || !allowed(rules.Restore)) {
				delete(cp, method)
				continue
			}
			if id == "createDocument" && createBody != alias+"Input" {
				createMapping := maps.Clone(mapping)
				createMapping["#/components/schemas/DocumentBody"] = "#/components/schemas/" + createBody
				rewriteRefs(op, createMapping)
			} else {
				rewriteRefs(op, mapping)
			}
			op["tags"] = []any{tag}
			b.tags[tag] = true
			if n, ok := opNames[id]; ok {
				op["operationId"] = n
			} else if id != "" {
				op["operationId"] = id + alias
			}
			dropParams(op, "realm", "database", "collection")
			orgParam(op, id, c)
			teamParam(op, id, c)
			expandParam(op, id, c)
			aggregateParams(op, id, c)
			if c.Text == nil {
				dropParams(op, "language") // only a collection with a text index can be searched
			}
		}
		if len(operations(cp)) == 0 {
			return
		}
		if idSchema != nil {
			typeIDParam(cp, idSchema)
		}
		b.uniqueIDs(cp, "")
		b.paths[path] = cp
	}
	suffixes := []string{"", "/{id}", "/{id}/restore"}
	if c.Aggregate != nil {
		suffixes = append(suffixes, "/_aggregate")
	}
	for _, suffix := range suffixes {
		if item := b.staticPath(prefix + suffix); item != nil {
			sub(item, base+suffix)
		}
	}
	// File endpoints, one set per file field.
	for _, field := range slices.Sorted(maps.Keys(c.Files)) {
		for _, suffix := range []string{
			"/_files/{field}", "/_files/{field}/uploads", "/{id}/_files/{field}/uploads",
			"/_files/{field}/uploads/{uploadID}/complete", "/{id}/_files/{field}", "/{id}/_files/{field}/{fileID}",
			"/{id}/_files/{field}/{fileID}/versions/{version}",
		} {
			item := b.staticPath(prefix + suffix)
			if item == nil {
				continue
			}
			cp := docutil.DeepCopy(item).(map[string]any)
			dropParams(cp, "fileField")
			for _, op := range operations(cp) {
				dropParams(op, "fileField")
			}
			path := base + strings.ReplaceAll(suffix, "{field}", field)
			b.addFileItem(path, tag, alias+strutil.Pascal(field), cp, mapping)
		}
	}
}

// typeIDParam gives the `id` path parameter of a path item and its operations the id schema of
// a collection with custom ids, so a client sees what an id looks like.
func typeIDParam(item, schema map[string]any) {
	typed := func(list any) any {
		params, ok := list.([]any)
		if !ok {
			return list
		}
		for i, p := range params {
			pm, ok := p.(map[string]any)
			if !ok || pm["$ref"] != "#/components/parameters/id" {
				continue
			}
			params[i] = map[string]any{"name": "id", "in": "path", "required": true, "schema": docutil.DeepCopy(schema)}
		}
		return params
	}
	if v, ok := item["parameters"]; ok {
		item["parameters"] = typed(v)
	}
	for _, op := range operations(item) {
		if v, ok := op["parameters"]; ok {
			op["parameters"] = typed(v)
		}
	}
}

// addFileItem registers a file endpoint's path item, typed like the collection's others.
func (b *builder) addFileItem(path, tag, alias string, cp map[string]any, mapping map[string]string) {
	dropParams(cp, "realm", "database", "collection", "fileField")
	for _, op := range operations(cp) {
		rewriteRefs(op, mapping)
		dropParams(op, "realm", "database", "collection", "fileField")
		op["tags"] = []any{tag}
		b.tags[tag] = true
	}
	b.uniqueIDs(cp, alias)
	b.paths[path] = cp
}

// function adds a function's route, with its schemas as the call's input and output.
func (b *builder) function(db string, fn *registry.Function) {
	item := b.staticPath("/v1/{realm}/{database}/_func/{function}")
	if item == nil {
		return
	}
	cp := docutil.DeepCopy(item).(map[string]any)
	dropParams(cp, "realm", "database", "function")
	alias := strutil.Pascal(db) + strutil.Pascal(fn.Name)
	tag := db + " functions"
	for _, op := range operations(cp) {
		dropParams(op, "realm", "database", "function")
		op["tags"] = []any{tag}
		op["operationId"] = b.claim("call" + alias)
		op["summary"] = "Call " + db + "/" + fn.Name
		if in := b.functionSchema(fn, registry.InputSchemaFile, alias+"Input"); in != "" && fn.Mode != registry.ModeWebhook {
			if rb, ok := op["requestBody"].(map[string]any); ok {
				if content, ok := rb["content"].(map[string]any); ok {
					content["application/json"] = map[string]any{"schema": map[string]any{"$ref": in}}
				}
			}
		}
		if out := b.functionSchema(fn, registry.OutputSchemaFile, alias+"Output"); out != "" && fn.Mode == registry.ModeSync {
			if responses, ok := op["responses"].(map[string]any); ok {
				if ok200, ok := responses["200"].(map[string]any); ok {
					ok200["content"] = map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": out}}}
				}
			}
		}
	}
	b.tags[tag] = true
	b.paths["/v1/"+b.realm+"/"+db+"/_func/"+fn.Name] = cp
}

// functionSchema registers a function's schema file as a component and returns its reference,
// or "" when the function has none.
func (b *builder) functionSchema(fn *registry.Function, file, name string) string {
	data, err := os.ReadFile(filepath.Join(fn.Dir, file))
	if err != nil {
		return ""
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return ""
	}
	b.toSchemas(name, raw)
	return "#/components/schemas/" + name
}

// configSchema declares the realm's configuration: the contract's `_config` answers values as
// an object, which the declared schema describes exactly.
func (b *builder) configSchema() {
	ac := b.rl.AppConfig
	if ac == nil {
		return
	}
	b.toSchemas("AppConfigValues", ac.RawSchema)
	item, ok := b.paths["/v1/"+b.realm+"/_config"].(map[string]any)
	if !ok {
		return
	}
	get, _ := item["get"].(map[string]any)
	responses, _ := get["responses"].(map[string]any)
	ok200, _ := responses["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	appJSON, _ := content["application/json"].(map[string]any)
	schema, _ := appJSON["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if values, ok := props["values"].(map[string]any); ok {
		for k := range values {
			delete(values, k)
		}
		values["$ref"] = "#/components/schemas/AppConfigValues"
	}
}

// orgParam gives an operation the `org` query parameter the collection's policy calls for: none
// for a collection without organizations, required on the create of one that requires them.
func orgParam(op map[string]any, id string, c *registry.Collection) {
	dropParams(op, "orgQuery")
	if !c.InOrganizations() {
		return
	}
	var required bool
	switch id {
	case "createDocument":
		required = c.Organization == registry.OrgRequired
	case "listDocuments", "batchWrite":
	default:
		return
	}
	param := map[string]any{
		"name": "org", "in": "query", "required": required, "schema": map[string]any{"type": "string"},
		"description": "The organization of the documents: where a create puts the document (the caller must be a member), or a filter on `_meta.org` for a list.",
	}
	op["parameters"] = append([]any{param}, anySlice(op["parameters"])...)
}

// teamParam is orgParam for `team`.
func teamParam(op map[string]any, id string, c *registry.Collection) {
	dropParams(op, "teamQuery")
	if !c.InTeams() || (id != "createDocument" && id != "batchWrite" && id != "listDocuments") {
		return
	}
	param := map[string]any{
		"name": "team", "in": "query", "required": c.Team == registry.TeamRequired && id == "createDocument", "schema": map[string]any{"type": "string"},
		"description": "The team of the documents: where a create puts the document (the caller must belong to the team or hold a team role in it, and name `org`), or a filter on `_meta.team` for a list.",
	}
	op["parameters"] = append([]any{param}, anySlice(op["parameters"])...)
}

// expandParam gives a read the `expand` parameter of the collection's own references: none for
// a collection that declares none, and the names it can take for one that does.
func expandParam(op map[string]any, id string, c *registry.Collection) {
	dropParams(op, "expand")
	if len(c.References) == 0 || (id != "getDocument" && id != "listDocuments") {
		return
	}
	var list []string
	for _, name := range slices.Sorted(maps.Keys(c.References)) {
		ref := c.References[name]
		list = append(list, fmt.Sprintf("`%s` (%s/%s)", name, ref.Database, ref.Collection))
	}
	param := map[string]any{
		"name": "expand", "in": "query", "required": false, "schema": map[string]any{"type": "string"},
		"description": "References to return in `_expanded`, with the fields to return of each in parentheses: `name(field,field)`, and `name.nested` for a reference of the referenced collection. This collection declares: " + strings.Join(list, ", ") + ".",
	}
	op["parameters"] = append(anySlice(op["parameters"]), param)
}

// aggregateParams tells an aggregation the fields its collection declares.
func aggregateParams(op map[string]any, id string, c *registry.Collection) {
	if id != "aggregateDocuments" || c.Aggregate == nil {
		return
	}
	for _, p := range anySlice(op["parameters"]) {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		switch pm["name"] {
		case "group_by":
			pm["description"] = fmt.Sprintf("%v Declared by this collection: %s (up to %d).", pm["description"], strings.Join(c.Aggregate.GroupBy, ", "), registry.MaxAggregateGroupBy)
		case "metrics":
			pm["description"] = fmt.Sprintf("%v Fields declared by this collection: %s.", pm["description"], strings.Join(c.Aggregate.Metrics, ", "))
		}
	}
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}
