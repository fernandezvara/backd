package typegen

import (
	"fmt"
	"go/format"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/strutil"
)

// GoTypesFile is the file `backd types --lang go` writes.
const GoTypesFile = "types.go"

// DefaultGoPackage is the package name of types.go when none is asked for.
const DefaultGoPackage = "backdtypes"

// goGen renders a realm's types as Go for pkg/client: a struct per document, write input and
// function input and output, string types with constants for enums, constants for the fields a
// where or an order_by may name, and typed accessors (a <Realm>Realm over the client, one
// wrapper per database) that hand out client.Typed collections and typed function calls.
type goGen struct {
	warnings []string
	used     map[string]string // declared name → what declared it (collisions)
	decls    strings.Builder
	emitted  map[string]bool
	// needs say which imports the file uses.
	needTime, needJSON, needCtx bool
	// collTypes remembers each collection's emitted doc/input/create names.
	collTypes map[string][3]string
	// fnTypes remembers each function's input and output Go types.
	fnTypes    map[string][2]string
	configTree string
	rl         *registry.Realm
	pkg        string
}

func newGoGen(pkg string) *goGen {
	if pkg == "" {
		pkg = DefaultGoPackage
	}
	return &goGen{used: map[string]string{}, emitted: map[string]bool{}, collTypes: map[string][3]string{}, fnTypes: map[string][2]string{}, pkg: pkg}
}

func (g *goGen) warn(format string, args ...any) {
	m := fmt.Sprintf(format, args...)
	if !slices.Contains(g.warnings, m) {
		g.warnings = append(g.warnings, m)
	}
}

// claim takes a name for what it names; a collision with a different thing gets a numeric
// suffix and a warning.
func (g *goGen) claim(name, what string) string {
	if prev, ok := g.used[name]; ok {
		if prev == what {
			return name
		}
		for i := 2; ; i++ {
			cand := fmt.Sprintf("%s%d", name, i)
			if _, ok := g.used[cand]; !ok {
				g.warn("%s: the name %s is already taken by %s; using %s", what, name, prev, cand)
				name = cand
				break
			}
		}
	}
	g.used[name] = what
	return name
}

func (g *goGen) emit(src string) {
	g.decls.WriteString(src)
	g.decls.WriteString("\n")
}

// initialisms are written in capitals in Go names: InvoiceID, not InvoiceId.
var initialisms = map[string]bool{"id": true, "url": true, "uri": true, "api": true, "ip": true, "uuid": true, "html": true, "json": true, "xml": true, "sql": true, "ui": true}

// goIdent renders a name as an exported Go identifier: its words capitalized, the common
// initialisms in capitals.
func goIdent(s string) string {
	var out strings.Builder
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') }) {
		if initialisms[strings.ToLower(w)] {
			out.WriteString(strings.ToUpper(w))
			continue
		}
		out.WriteString(strutil.Pascal(w))
	}
	if out.Len() == 0 {
		return "X"
	}
	return out.String()
}

// goLit renders a string as a Go literal.
func goLit(s string) string { return strconv.Quote(s) }

// goDoc renders a comment, one line per line of text.
func goDoc(indent, text string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		l = strings.ReplaceAll(l, "*/", "* /")
		b.WriteString(indent + "// " + l + "\n")
	}
	return b.String()
}

// goConverter turns JSON Schema documents into Go types, emitting the named structs, string
// types and aliases the schema needs into the goGen.
type goConverter struct {
	g      *goGen
	root   map[string]any
	prefix string
	where  string
	refs   map[string]string
}

func newGoConverter(root map[string]any, prefix, where string, g *goGen) *goConverter {
	return &goConverter{g: g, root: root, prefix: prefix, where: where, refs: map[string]string{}}
}

// goType is a Go type and whether the schema admits null.
type goType struct {
	expr     string
	nullable bool
}

// reference types already have a nil value and need no pointer.
func (t goType) byReference() bool {
	return strings.HasPrefix(t.expr, "[]") || strings.HasPrefix(t.expr, "map[") || t.expr == "any" || t.expr == "json.RawMessage"
}

// typeOf is the Go type of a schema. hint names the type the schema gets when it becomes one.
func (c *goConverter) typeOf(s any, path, hint string) goType {
	switch t := s.(type) {
	case bool:
		return goType{expr: "any", nullable: true}
	case map[string]any:
		return c.typeOfMap(t, path, hint)
	}
	return goType{expr: "any", nullable: true}
}

func (c *goConverter) kind(s map[string]any) string {
	if _, ok := str(s["x-backd-ts"]); ok {
		return "file"
	}
	if _, ok := str(s["$ref"]); ok {
		return "ref"
	}
	if isStringEnum(s) {
		return "enum"
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if arr, ok := s[kw].([]any); ok && len(arr) > 0 {
			return "union"
		}
	}
	return "plain"
}

// isStringEnum reports whether the schema is an enum (or const) whose values are all strings.
func isStringEnum(s map[string]any) bool {
	var vals []any
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		vals = e
	} else if v, ok := s["const"]; ok {
		vals = []any{v}
	} else {
		return false
	}
	for _, v := range vals {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

func (c *goConverter) typeOfMap(s map[string]any, path, hint string) goType {
	for _, k := range unsupported {
		if _, ok := s[k]; ok {
			c.g.warn("%s%s: keyword %q has no Go equivalent; typed any", c.where, path, k)
			return goType{expr: "any", nullable: true}
		}
	}
	if t, ok := str(s["x-backd-ts"]); ok {
		switch t {
		case "FileDetails":
			return goType{expr: "client.FileDetails"}
		case "FileDetails[]":
			return goType{expr: "[]client.FileDetails"}
		}
		c.g.warn("%s%s: x-backd-ts %q unknown to the Go emitter; typed any", c.where, path, t)
		return goType{expr: "any", nullable: true}
	}
	if ref, ok := str(s["$ref"]); ok {
		return c.ref(ref, path)
	}
	if isStringEnum(s) {
		return goType{expr: c.enumType(s, path, hint), nullable: typeHasNull(s)}
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if arr, ok := s[kw].([]any); ok && len(arr) > 0 {
			c.g.warn("%s%s: %s has no Go equivalent; typed json.RawMessage (decode it by hand)", c.where, path, kw)
			c.g.needJSON = true
			return goType{expr: "json.RawMessage", nullable: true}
		}
	}
	types := typeList(s["type"])
	if len(types) == 0 {
		switch {
		case s["properties"] != nil, s["additionalProperties"] != nil, s["required"] != nil:
			types = []string{"object"}
		case s["items"] != nil, s["prefixItems"] != nil:
			types = []string{"array"}
		default:
			return goType{expr: "any", nullable: true}
		}
	}
	var nonNull []string
	for _, t := range types {
		if t != "null" {
			nonNull = append(nonNull, t)
		}
	}
	switch len(nonNull) {
	case 0:
		return goType{expr: "any", nullable: true}
	case 1:
		t := c.single(s, nonNull[0], path, hint)
		t.nullable = t.nullable || len(types) > len(nonNull)
		return t
	}
	c.g.warn("%s%s: a union of JSON types %v has no Go equivalent; typed any", c.where, path, nonNull)
	return goType{expr: "any", nullable: true}
}

func typeHasNull(s map[string]any) bool { return slices.Contains(typeList(s["type"]), "null") }

func (c *goConverter) single(s map[string]any, t, path, hint string) goType {
	switch t {
	case "string":
		if f, _ := str(s["format"]); f == "date-time" {
			c.g.needTime = true
			return goType{expr: "time.Time"}
		}
		return goType{expr: "string"}
	case "integer":
		return goType{expr: "int64"}
	case "number":
		return goType{expr: "float64"}
	case "boolean":
		return goType{expr: "bool"}
	case "array":
		if pre, ok := s["prefixItems"].([]any); ok && len(pre) > 0 {
			c.g.warn("%s%s: tuple types have no Go equivalent; typed []any", c.where, path)
			return goType{expr: "[]any", nullable: true}
		}
		if it, ok := s["items"]; ok {
			e := c.typeOf(it, path+"/items", hint+"Item")
			return goType{expr: "[]" + c.field(e, true), nullable: true}
		}
		return goType{expr: "[]any", nullable: true}
	case "object":
		if props, _ := s["properties"].(map[string]any); len(props) > 0 {
			return goType{expr: c.class(s, path, hint)}
		}
		if ap, ok := s["additionalProperties"].(map[string]any); ok {
			e := c.typeOf(ap, path+"/additionalProperties", hint+"Value")
			return goType{expr: "map[string]" + c.field(e, true), nullable: true}
		}
		return goType{expr: "map[string]any", nullable: true}
	}
	return goType{expr: "any", nullable: true}
}

// field renders a type as a struct field, a slice element or a map value: a pointer when the value
// may be absent or null and Go has no nil value for it.
func (c *goConverter) field(t goType, required bool) string {
	if (!required || t.nullable) && !t.byReference() {
		return "*" + t.expr
	}
	return t.expr
}

// ref names the type a local $ref points to, emitting it on first use.
func (c *goConverter) ref(ref, path string) goType {
	if !strings.HasPrefix(ref, "#") {
		c.g.warn("%s%s: $ref %q is not local; typed any", c.where, path, ref)
		return goType{expr: "any", nullable: true}
	}
	target := resolvePointer(c.root, strings.TrimPrefix(ref, "#"))
	if target == nil {
		c.g.warn("%s%s: $ref %q doesn't resolve; typed any", c.where, path, ref)
		return goType{expr: "any", nullable: true}
	}
	name, seen := c.refs[ref]
	if !seen {
		last := ref[strings.LastIndex(ref, "/")+1:]
		name = c.g.claim(c.prefix+goIdent(last), c.where+"$ref "+ref)
		if ref == "#" {
			name = c.g.claim(c.prefix+"Self", c.where+"$ref #")
		}
		c.refs[ref] = name
	}
	m, isMap := target.(map[string]any)
	if isMap && (isObject(m) || isStringEnum(m)) {
		if !c.g.emitted[name] {
			c.g.emitted[name] = true
			if isObject(m) {
				c.emitStruct(name, m, path, "", "")
			} else {
				c.emitEnum(name, m)
			}
		}
		return goType{expr: name, nullable: isMap && typeHasNull(m)}
	}
	// a reference to a scalar, a list or a map is that type under a name
	if !c.g.emitted[name] {
		c.g.emitted[name] = true
		inner := c.typeOf(target, path, name)
		c.g.emit(fmt.Sprintf("type %s = %s\n", name, inner.expr))
		return goType{expr: name, nullable: inner.nullable}
	}
	return goType{expr: name}
}

// class emits a struct for an object schema and answers its name; the same hint converting the same
// schema twice emits it once.
func (c *goConverter) class(s map[string]any, path, hint string) string {
	name := c.g.claim(hint, c.where+path)
	if c.g.emitted[name] {
		return name
	}
	c.g.emitted[name] = true
	c.emitStruct(name, s, path, "", "")
	return name
}

// emitStruct writes the struct of an object schema. embed is "doc" (a document: it embeds
// client.Base, which holds id, _meta and _expanded) or "" (plain). doc is the comment above it.
func (c *goConverter) emitStruct(name string, s map[string]any, path, embed, doc string) {
	c.emitStructHint(name, name, s, path, embed, doc)
}

// emitStructHint is emitStruct with the name the types of its fields are named from: a collection's
// document and its input share the nested types, so both name them from the collection.
func (c *goConverter) emitStructHint(name, hint string, s map[string]any, path, embed, doc string) {
	props, required := propsOf(s)
	fields := map[string]string{}
	for _, n := range slices.Sorted(keys(props)) {
		fields[n] = c.field(c.typeOf(props[n], path+"/properties/"+n, hint+goIdent(n)), required[n])
	}
	var b strings.Builder
	if doc == "" {
		if d, _ := str(s["description"]); d != "" {
			doc = name + " — " + d
		}
	}
	if doc != "" {
		b.WriteString(goDoc("", doc))
	}
	fmt.Fprintf(&b, "type %s struct {\n", name)
	if embed == "doc" {
		b.WriteString("\tclient.Base\n")
	}
	used := map[string]bool{"Base": embed == "doc"}
	for _, n := range slices.Sorted(keys(props)) {
		id := goIdent(n)
		for used[id] {
			id += "_"
		}
		used[id] = true
		tag := n
		if !required[n] {
			tag += ",omitempty"
		}
		if d, _ := str(mapOf(props[n])["description"]); d != "" {
			b.WriteString(goDoc("\t", d))
		}
		fmt.Fprintf(&b, "\t%s %s `json:%s`\n", id, fields[n], goLit(tag))
	}
	b.WriteString("}\n")
	c.g.emit(b.String())
}

func mapOf(v any) map[string]any { m, _ := v.(map[string]any); return m }

// enumType emits a string type with a constant per value and answers its name.
func (c *goConverter) enumType(s map[string]any, path, hint string) string {
	name := c.g.claim(hint, c.where+path)
	if !c.g.emitted[name] {
		c.g.emitted[name] = true
		c.emitEnum(name, s)
	}
	return name
}

func (c *goConverter) emitEnum(name string, s map[string]any) {
	var vals []any
	if e, ok := s["enum"].([]any); ok {
		vals = e
	} else if v, ok := s["const"]; ok {
		vals = []any{v}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s is one of the values below.\ntype %s string\n\nconst (\n", name, name)
	used := map[string]bool{}
	seen := map[string]bool{}
	for _, v := range vals {
		sv, _ := v.(string)
		if seen[sv] {
			continue // a value listed twice is one value
		}
		seen[sv] = true
		id := name + goIdent(sv)
		for used[id] || (c.g.used[id] != "" && c.g.used[id] != "value of "+name) {
			id += "Value"
		}
		used[id] = true
		c.g.used[id] = "value of " + name
		fmt.Fprintf(&b, "\t%s %s = %s\n", id, name, goLit(sv))
	}
	b.WriteString(")\n")
	c.g.emit(b.String())
}

// realm renders the whole file for a realm.
func (g *goGen) realm(rl *registry.Realm, o Options, header string) string {
	g.rl = rl
	dbs := make([]string, 0, len(rl.Databases))
	for n := range rl.Databases {
		if o.Database == "" || n == o.Database {
			dbs = append(dbs, n)
		}
	}
	sort.Strings(dbs)
	prefix := goIdent(rl.Name)

	collPrefix := map[string]string{}
	for _, dbName := range dbs {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			collPrefix[dbName+"/"+c.Name] = g.claim(goIdent(dbName)+goIdent(c.Name), "collection "+dbName+"/"+c.Name)
		}
	}
	for _, dbName := range dbs {
		db := rl.Databases[dbName]
		for _, c := range db.SortedCollections() {
			g.collection(dbName, c, collPrefix[dbName+"/"+c.Name])
		}
		if db.Functions != nil {
			for _, name := range slices.Sorted(keys(db.Functions.Functions)) {
				g.function(dbName, db.Functions.Functions[name])
			}
		}
	}
	if ac := rl.AppConfig; ac != nil {
		g.config(prefix, ac)
	}
	g.roleConsts(prefix, rl)

	var tail strings.Builder
	realmName := g.claim(prefix+"Realm", "realm "+rl.Name)
	fmt.Fprintf(&tail, "// %s is the %q realm bound to a client: its databases, collections and functions, typed.\ntype %s struct{ Client *client.Client }\n\n", realmName, rl.Name, realmName)
	fmt.Fprintf(&tail, "// New%s binds the realm's types to a client made for the realm.\nfunc New%s(c *client.Client) *%s { return &%s{Client: c} }\n", realmName, realmName, realmName, realmName)
	dbTypes := map[string]string{}
	for _, dbName := range dbs {
		dbTypes[dbName] = g.claim(prefix+goIdent(dbName), "database "+dbName)
	}
	for _, dbName := range dbs {
		db := rl.Databases[dbName]
		name := dbTypes[dbName]
		fmt.Fprintf(&tail, "\n// %s is the %q database of %q, typed.\ntype %s struct{ db *client.Database }\n\n", name, dbName, rl.Name, name)
		fmt.Fprintf(&tail, "// %s returns the %q database.\nfunc (r *%s) %s() *%s { return &%s{db: r.Client.DB(%s)} }\n\n", goIdent(dbName), dbName, realmName, goIdent(dbName), name, name, goLit(dbName))
		fmt.Fprintf(&tail, "// DB returns the plain database.\nfunc (d *%s) DB() *client.Database { return d.db }\n", name)
		methods := map[string]bool{"DB": true}
		for _, c := range db.SortedCollections() {
			docT := g.collTypes[dbName+"/"+c.Name][0]
			m := goIdent(c.Name)
			for methods[m] {
				m += "Collection"
			}
			methods[m] = true
			fmt.Fprintf(&tail, "\n// %s is the %q collection: documents read as %s.\nfunc (d *%s) %s() *client.Typed[%s] {\n\treturn client.As[%s](d.db.Collection(%s))\n}\n", m, c.Name, docT, name, m, docT, docT, goLit(c.Name))
		}
		if db.Functions != nil {
			for _, fn := range slices.Sorted(keys(db.Functions.Functions)) {
				f := db.Functions.Functions[fn]
				if f.Internal {
					continue
				}
				m := goIdent(fn)
				for methods[m] {
					m += "Fn"
				}
				methods[m] = true
				g.functionMethod(&tail, name, m, dbName, f)
			}
		}
	}
	if rl.AppConfig != nil {
		g.needCtx, g.needJSON = true, true
		fmt.Fprintf(&tail, "\n// LoadConfig reads the realm's app configuration, as the caller sees it.\nfunc (r *%s) LoadConfig(ctx context.Context, opts ...client.RequestOption) (*%s, error) {\n\tvalues, err := r.Client.Config().Load(ctx, nil, opts...)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\traw, err := json.Marshal(values)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tvar out %s\n\tif err := json.Unmarshal(raw, &out); err != nil {\n\t\treturn nil, err\n\t}\n\treturn &out, nil\n}\n", realmName, g.configTree, g.configTree)
	}

	var imports []string
	if g.needCtx {
		imports = append(imports, `"context"`)
	}
	if g.needJSON {
		imports = append(imports, `"encoding/json"`)
	}
	if g.needTime {
		imports = append(imports, `"time"`)
	}
	imp := "import (\n"
	for _, i := range imports {
		imp += "\t" + i + "\n"
	}
	if len(imports) > 0 {
		imp += "\n"
	}
	imp += "\t\"github.com/fernandezvara/backd/pkg/client\"\n)\n"
	src := header + "package " + g.pkg + "\n\n" + imp + "\n" + g.decls.String() + tail.String()
	formatted, err := format.Source([]byte(src))
	if err != nil {
		g.warn("the Go output could not be formatted: %v", err)
		return src
	}
	return string(formatted)
}

// collection emits one collection's types.
func (g *goGen) collection(db string, c *registry.Collection, prefix string) {
	where0 := db + "/" + c.Name + " schema.json "
	docSchema, inputSchema := fileAware(c)
	conv := newGoConverter(docSchema, prefix, where0, g)
	inConv := newGoConverter(inputSchema, prefix, where0, g)

	docName := prefix // claimed with the collection's prefix
	inName := g.claim(prefix+"Input", "input "+db+"/"+c.Name)
	createName := ""
	g.collTypes[db+"/"+c.Name] = [3]string{docName, inName, createName}

	g.structOrMap(conv, docName, docSchema, "doc", fmt.Sprintf("%s is a %s/%s document.", docName, db, c.Name))
	g.structOrMap(inConv, inName, inputSchema, "", fmt.Sprintf("%s is what a %s/%s write accepts.", inName, db, c.Name))
	if c.CustomID != nil {
		createName = g.claim(prefix+"Create", "create "+db+"/"+c.Name)
		g.collTypes[db+"/"+c.Name] = [3]string{docName, inName, createName}
		note := "the id is chosen by the client and never changes; it must match " + c.CustomID.Anchored()
		if c.CustomID.User {
			note = "the id is chosen by the client and never changes; it must be the id of the signed-in user"
		}
		doc := fmt.Sprintf("%s is what creating a %s/%s document takes: %s.", createName, db, c.Name, note)
		if isObject(inputSchema) {
			g.emit(goDoc("", doc) + fmt.Sprintf("type %s struct {\n\tID string `json:\"id\"`\n\t%s\n}\n", createName, inName))
		} else {
			g.emit(goDoc("", doc+" The document must carry \"id\".") + fmt.Sprintf("type %s = map[string]any\n", createName))
		}
	}
	g.queryConsts(db, c, prefix)
	if c.Aggregate != nil {
		g.aggregateConsts(c, prefix)
	}
}

// structOrMap emits an object schema as a struct, or a schema with no declared properties as an
// alias (client.Doc for a document, a map for a write).
func (g *goGen) structOrMap(conv *goConverter, name string, schema map[string]any, embed, doc string) {
	g.emitted[name] = true
	if !isObject(schema) {
		alias := "map[string]any"
		if embed == "doc" {
			alias = "client.Doc"
		}
		g.emit(goDoc("", doc) + fmt.Sprintf("type %s = %s\n", name, alias))
		return
	}
	conv.emitStructHint(name, conv.prefix, schema, "", embed, doc)
}

// queryConsts emits the fields a where or an order_by of the collection may name.
func (g *goGen) queryConsts(db string, c *registry.Collection, prefix string) {
	fields := append([]queryField{}, systemQueryFields...)
	if c.InOrganizations() {
		fields = append(fields, orgQueryFields...)
	}
	if c.InTeams() {
		fields = append(fields, teamQueryFields...)
	}
	if c.SoftDelete != nil {
		fields = append(fields, softDeleteQueryFields...)
	}
	for _, p := range slices.Sorted(keys(c.Fields)) {
		fields = append(fields, fieldOf(p, c.Fields[p], schemaFor(c.RawSchema, p)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// The fields the where and the order_by of %s/%s may name; the Desc constants sort descending.\nconst (\n", db, c.Name)
	used := map[string]bool{}
	for _, f := range fields {
		if f.kind == kindOpaque {
			continue
		}
		id := prefix + "Field" + goIdent(strings.NewReplacer(".", "_").Replace(f.path))
		for used[id] || g.used[id] != "" {
			id += "X"
		}
		used[id] = true
		g.used[id] = "field " + f.path
		fmt.Fprintf(&b, "\t%s = %s\n\t%sDesc = %s\n", id, goLit(f.path), id, goLit("-"+f.path))
	}
	b.WriteString(")\n")
	g.emit(b.String())
}

// aggregateConsts emits what an aggregate of a collection may use.
func (g *goGen) aggregateConsts(c *registry.Collection, prefix string) {
	a := c.Aggregate
	name := g.claim(prefix+"Aggregate", "aggregate "+c.Name)
	var dates []string
	for _, f := range a.GroupBy {
		if f == "_meta.created_at" || f == "_meta.updated_at" || c.Fields[f].Date {
			dates = append(dates, f)
		}
	}
	list := func(l []string) string {
		parts := make([]string, len(l))
		for i, f := range l {
			parts[i] = goLit(f)
		}
		return "[]string{" + strings.Join(parts, ", ") + "}"
	}
	g.emit(goDoc("", fmt.Sprintf("%s is what an aggregate of %s may use: the group-by fields, those of them that are dates\n(they can be bucketed \"field:day\"), and the metrics.", name, c.Name)) +
		fmt.Sprintf("var %s = struct{ GroupBy, Dates, Metrics []string }{\n\tGroupBy: %s,\n\tDates:   %s,\n\tMetrics: %s,\n}\n", name, list(a.GroupBy), list(dates), list(a.Metrics)))
}

// function emits a function's input and output types.
func (g *goGen) function(db string, fn *registry.Function) {
	in, _ := readFunctionSchema(fn.Dir, registry.InputSchemaFile)
	out, _ := readFunctionSchema(fn.Dir, registry.OutputSchemaFile)
	prefix := "Fn" + goIdent(db) + goIdent(fn.Name)
	key := db + "/" + fn.Name
	var inT, outT string
	if in != nil {
		conv := newGoConverter(in, prefix, key+" "+registry.InputSchemaFile+" ", g)
		inT = g.claim(prefix+"Input", "function "+key+" input")
		g.typeDecl(conv, inT, in, "The input of "+key+".")
	}
	if out != nil {
		conv := newGoConverter(out, prefix, key+" "+registry.OutputSchemaFile+" ", g)
		outT = g.claim(prefix+"Output", "function "+key+" output")
		g.typeDecl(conv, outT, out, "The output of "+key+".")
	}
	g.fnTypes[key] = [2]string{inT, outT}
}

// typeDecl declares a named type for a schema: a struct for an object, else an alias of its Go type.
func (g *goGen) typeDecl(conv *goConverter, name string, schema map[string]any, doc string) {
	if isObject(schema) {
		g.structOrMap(conv, name, schema, "", doc)
		return
	}
	g.emitted[name] = true
	t := conv.typeOf(schema, "", name)
	g.emit(goDoc("", doc) + fmt.Sprintf("type %s = %s\n", name, t.expr))
}

// functionMethod writes a typed call of a non-internal function on the database wrapper.
func (g *goGen) functionMethod(w *strings.Builder, wrapper, method, db string, fn *registry.Function) {
	g.needCtx = true
	ts := g.fnTypes[db+"/"+fn.Name]
	inT, outT := ts[0], ts[1]
	if inT == "" {
		inT = "any"
	}
	if fn.Mode == registry.ModeAsync {
		fmt.Fprintf(w, "\n// %s calls %s/%s (async: it answers a job; Wait for the output).\nfunc (d *%s) %s(ctx context.Context, in %s, opts ...client.RequestOption) (*client.Job, error) {\n\treturn d.db.Call(ctx, %s, in, nil, append([]client.RequestOption{client.WithRespondAsync()}, opts...)...)\n}\n",
			method, db, fn.Name, wrapper, method, inT, goLit(fn.Name))
		return
	}
	if outT == "" {
		g.needJSON = true
		outT = "json.RawMessage"
	}
	fmt.Fprintf(w, "\n// %s calls %s/%s.\nfunc (d *%s) %s(ctx context.Context, in %s, opts ...client.RequestOption) (*%s, error) {\n\tvar out %s\n\tif _, err := d.db.Call(ctx, %s, in, &out, opts...); err != nil {\n\t\treturn nil, err\n\t}\n\treturn &out, nil\n}\n",
		method, db, fn.Name, wrapper, method, inT, outT, outT, goLit(fn.Name))
}

// config emits the configuration's tree struct and the constants of its leaf paths.
func (g *goGen) config(prefix string, ac *registry.AppConfig) {
	conv := newGoConverter(ac.RawSchema, prefix+"Config", "_realm/config/schema.json ", g)
	name := g.claim(prefix+"ConfigTree", "config tree")
	g.configTree = name
	g.typeDecl(conv, name, ac.RawSchema, name+" is the realm's app configuration.")
	var b strings.Builder
	fmt.Fprintf(&b, "// The app configuration's leaf paths, for client.Config().Get.\nconst (\n")
	used := map[string]bool{}
	for _, p := range slices.Sorted(keys(ac.Paths)) {
		if !ac.Paths[p].Leaf {
			continue
		}
		id := prefix + "ConfigPath" + goIdent(strings.NewReplacer(".", "_").Replace(p))
		for used[id] || g.used[id] != "" {
			id += "X"
		}
		used[id] = true
		g.used[id] = "config path " + p
		fmt.Fprintf(&b, "\t%s = %s\n", id, goLit(p))
	}
	b.WriteString(")\n")
	g.emit(b.String())
}

// roleConsts emits the realm's role, organization role, team role and kind names as constants.
func (g *goGen) roleConsts(prefix string, rl *registry.Realm) {
	consts := func(name, what string, values []string) {
		if len(values) == 0 {
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "// The %s %q declares.\nconst (\n", what, rl.Name)
		used := map[string]bool{}
		for _, v := range values {
			id := prefix + name + goIdent(v)
			for used[id] || g.used[id] != "" {
				id += "Value"
			}
			used[id] = true
			g.used[id] = what + " " + v
			fmt.Fprintf(&b, "\t%s = %s\n", id, goLit(v))
		}
		b.WriteString(")\n")
		g.emit(b.String())
	}
	consts("Role", "roles", slices.Sorted(keys(rl.Settings.Roles)))
	if o := rl.Settings.Organizations; o != nil {
		consts("OrgRole", "organization roles", o.RoleNames())
		consts("TeamRole", "team roles", o.TeamRoleNames())
	}
	if rl.Email != nil {
		consts("EmailKind", "email kinds", rl.Email.Kinds())
	}
	if rl.Push != nil {
		consts("PushKind", "push kinds", rl.Push.Kinds())
	}
}
