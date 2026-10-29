package typegen

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/strutil"
)

// PythonTypesFile is the file `backd types --lang python` writes.
const PythonTypesFile = "types.py"

// pyGen renders a realm's types as Python for clients/python: a TypedDict per document, write input and
// function input and output (required fields in one class, optional ones in another, so it runs on Python
// 3.10), Literal types for enums, classes of constants for the fields a where or an order_by may name, and
// typed wrappers of the sync and the async client that hand out typed collections and functions.
type pyGen struct {
	warnings []string
	used     map[string]string // declared name → what declared it (collisions)
	decls    strings.Builder
	emitted  map[string]bool
	// needs say which names of typing the file uses.
	needCast, needLiteral bool
	// collTypes remembers each collection's emitted doc/input/create names.
	collTypes map[string][3]string
	// fnTypes remembers each function's input and output Python types.
	fnTypes    map[string][2]string
	configTree string
	rl         *registry.Realm
}

func newPyGen() *pyGen {
	return &pyGen{used: map[string]string{}, emitted: map[string]bool{}, collTypes: map[string][3]string{}, fnTypes: map[string][2]string{}}
}

func (g *pyGen) warn(format string, args ...any) {
	m := fmt.Sprintf(format, args...)
	if !slices.Contains(g.warnings, m) {
		g.warnings = append(g.warnings, m)
	}
}

// claim takes a name for what it names; a collision with a different thing gets a numeric suffix and a warning.
func (g *pyGen) claim(name, what string) string {
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

func (g *pyGen) emit(src string) {
	g.decls.WriteString(src)
	g.decls.WriteString("\n\n\n")
}

var pyKeywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields("False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield match case type") {
		pyKeywords[k] = true
	}
}

// pyIdent renders a name as a Pascal-case Python class name.
func pyIdent(s string) string {
	var out strings.Builder
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') }) {
		out.WriteString(strutil.Pascal(w))
	}
	if out.Len() == 0 {
		return "X"
	}
	if r := rune(out.String()[0]); unicode.IsDigit(r) {
		return "X" + out.String()
	}
	return out.String()
}

// pyConst renders a path as an UPPER_SNAKE constant name.
func pyConst(s string) string {
	var out strings.Builder
	var prev rune
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			out.WriteRune(unicode.ToUpper(r))
		case r >= 'A' && r <= 'Z':
			if prev >= 'a' && prev <= 'z' {
				out.WriteByte('_')
			}
			out.WriteRune(r)
		case r >= '0' && r <= '9':
			out.WriteRune(r)
		default:
			if out.Len() > 0 && !strings.HasSuffix(out.String(), "_") {
				out.WriteByte('_')
			}
		}
		prev = r
	}
	name := strings.Trim(out.String(), "_")
	if name == "" {
		return "X"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "X_" + name
	}
	return name
}

// pyFieldName reports whether a JSON property name can be written as a class attribute.
func pyFieldName(n string) bool {
	if n == "" || pyKeywords[n] || strings.HasPrefix(n, "__") {
		return false
	}
	for i, r := range n {
		letter := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit := i > 0 && r >= '0' && r <= '9'
		if !letter && !digit {
			return false
		}
	}
	return true
}

func pyLit(s string) string { return strconv.Quote(s) }

// pyDoc renders a docstring body, indented.
func pyDoc(indent, text string) string {
	text = strings.ReplaceAll(strings.TrimSpace(text), `\`, `\\`)
	text = strings.ReplaceAll(text, `"""`, `\"\"\"`)
	if strings.HasSuffix(text, `"`) {
		text += " "
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 1 {
		return indent + `"""` + lines[0] + `"""` + "\n"
	}
	var b strings.Builder
	b.WriteString(indent + `"""` + lines[0] + "\n")
	for _, l := range lines[1:] {
		if l == "" {
			b.WriteString("\n")
		} else {
			b.WriteString(indent + l + "\n")
		}
	}
	b.WriteString(indent + `"""` + "\n")
	return b.String()
}

func pyComment(indent, text string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		b.WriteString(indent + "#: " + l + "\n")
	}
	return b.String()
}

// pyField is one field of a TypedDict.
type pyField struct {
	key      string
	typ      string
	required bool
	doc      string
}

// typedDict emits the TypedDict classes of an object: its required fields in one class and its optional ones in
// another (Python 3.10 has no NotRequired), and when there are both, or a base, the class that joins them.
// base is "" or "backd.Doc".
func (g *pyGen) typedDict(name, base string, fields []pyField, doc string) {
	var req, opt []pyField
	for _, f := range fields {
		if f.required {
			req = append(req, f)
		} else {
			opt = append(opt, f)
		}
	}
	group := func(gname string, fs []pyField, total bool, bases string) string {
		clean := true
		for _, f := range fs {
			clean = clean && pyFieldName(f.key)
		}
		if clean {
			var b strings.Builder
			totalArg := ""
			if !total {
				totalArg = ", total=False"
			}
			if bases == "" {
				bases = "TypedDict"
			}
			fmt.Fprintf(&b, "class %s(%s%s):\n", gname, bases, totalArg)
			return b.String()
		}
		return ""
	}
	body := func(fs []pyField) string {
		var b strings.Builder
		for _, f := range fs {
			if f.doc != "" {
				b.WriteString(pyComment("    ", f.doc))
			}
			fmt.Fprintf(&b, "    %s: %s\n", f.key, f.typ)
		}
		return b.String()
	}
	// A field name Python can't write (a keyword, a dash) needs the functional form.
	functional := func(gname string, fs []pyField, total bool) string {
		var parts []string
		for _, f := range fs {
			parts = append(parts, pyLit(f.key)+": "+pyLit(f.typ))
		}
		totalArg := ""
		if !total {
			totalArg = ", total=False"
		}
		return fmt.Sprintf("%s = TypedDict(%s, {%s}%s)\n", gname, pyLit(gname), strings.Join(parts, ", "), totalArg)
	}
	render := func(gname string, fs []pyField, total bool, bases string, docText string) string {
		if len(fs) == 0 && bases == "" {
			head := group(gname, nil, total, "")
			out := head
			if docText != "" {
				out += pyDoc("    ", docText)
			} else {
				out += "    pass\n"
			}
			return out
		}
		if head := group(gname, fs, total, bases); head != "" {
			out := head
			if docText != "" {
				out += pyDoc("    ", docText)
			}
			if len(fs) == 0 && docText == "" {
				out += "    pass\n"
			}
			return out + body(fs)
		}
		// the functional form can't have bases: the fields go in a class of their own that this one extends
		fname := "_" + strings.TrimPrefix(gname, "_") + "Fields"
		out := functional(fname, fs, total) + "\n\n"
		b := fname
		if bases != "" {
			b = bases + ", " + fname
		}
		out += fmt.Sprintf("class %s(%s):\n", gname, b)
		if docText != "" {
			out += pyDoc("    ", docText)
		} else {
			out += "    pass\n"
		}
		return out
	}

	var out strings.Builder
	switch {
	case base == "" && (len(req) == 0 || len(opt) == 0):
		fs, total := req, true
		if len(req) == 0 && len(opt) > 0 {
			fs, total = opt, false
		}
		out.WriteString(render(name, fs, total, "", doc))
	default:
		var bases []string
		if base != "" {
			bases = append(bases, base)
		}
		if len(req) > 0 {
			rn := "_" + name + "Required"
			out.WriteString(render(rn, req, true, "", "") + "\n\n")
			bases = append(bases, rn)
		}
		if len(opt) > 0 {
			on := "_" + name + "Optional"
			out.WriteString(render(on, opt, false, "", "") + "\n\n")
			bases = append(bases, on)
		}
		out.WriteString(render(name, nil, true, strings.Join(bases, ", "), doc))
	}
	g.emit(strings.TrimRight(out.String(), "\n"))
}

// pyConverter turns JSON Schema documents into Python types, emitting the named classes and aliases the
// schema needs into the pyGen.
type pyConverter struct {
	g      *pyGen
	root   map[string]any
	prefix string
	where  string
	refs   map[string]string
}

func newPyConverter(root map[string]any, prefix, where string, g *pyGen) *pyConverter {
	return &pyConverter{g: g, root: root, prefix: prefix, where: where, refs: map[string]string{}}
}

// pyType is a Python type and whether the schema admits null.
type pyType struct {
	expr     string
	nullable bool
}

// render is the annotation of a value that is null when the schema admits it.
func (t pyType) render() string {
	if t.nullable && t.expr != "Any" && t.expr != "None" {
		return t.expr + " | None"
	}
	return t.expr
}

func (c *pyConverter) typeOf(s any, path, hint string) pyType {
	switch t := s.(type) {
	case bool:
		return pyType{expr: "Any", nullable: true}
	case map[string]any:
		return c.typeOfMap(t, path, hint)
	}
	return pyType{expr: "Any", nullable: true}
}

func (c *pyConverter) typeOfMap(s map[string]any, path, hint string) pyType {
	for _, k := range unsupported {
		if _, ok := s[k]; ok {
			c.g.warn("%s%s: keyword %q has no Python equivalent; typed Any", c.where, path, k)
			return pyType{expr: "Any", nullable: true}
		}
	}
	if t, ok := str(s["x-backd-ts"]); ok {
		switch t {
		case "FileDetails":
			return pyType{expr: "backd.FileDetails"}
		case "FileDetails[]":
			return pyType{expr: "list[backd.FileDetails]"}
		}
		c.g.warn("%s%s: x-backd-ts %q unknown to the Python emitter; typed Any", c.where, path, t)
		return pyType{expr: "Any", nullable: true}
	}
	if ref, ok := str(s["$ref"]); ok {
		return c.ref(ref, path)
	}
	if lit, ok := c.literalType(s); ok {
		return lit
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if arr, ok := s[kw].([]any); ok && len(arr) > 0 {
			var parts []string
			nullable := false
			for i, branch := range arr {
				t := c.typeOf(branch, fmt.Sprintf("%s/%s/%d", path, kw, i), fmt.Sprintf("%sOption%d", hint, i+1))
				if t.expr == "Any" {
					return pyType{expr: "Any", nullable: true}
				}
				nullable = nullable || t.nullable
				parts = append(parts, t.expr)
			}
			return pyType{expr: strings.Join(unique(parts), " | "), nullable: nullable}
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
			return pyType{expr: "Any", nullable: true}
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
		return pyType{expr: "Any", nullable: true}
	case 1:
		t := c.single(s, nonNull[0], path, hint)
		t.nullable = t.nullable || len(types) > len(nonNull)
		return t
	}
	// a union of JSON types is a union of their Python types
	var parts []string
	for _, t := range nonNull {
		parts = append(parts, c.single(s, t, path, hint).expr)
	}
	return pyType{expr: strings.Join(unique(parts), " | "), nullable: len(types) > len(nonNull)}
}

// literalType renders an enum (or a const) of scalars as a Literal.
func (c *pyConverter) literalType(s map[string]any) (pyType, bool) {
	var vals []any
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		vals = e
	} else if v, ok := s["const"]; ok {
		vals = []any{v}
	} else {
		return pyType{}, false
	}
	var parts []string
	nullable := false
	for _, v := range vals {
		switch x := v.(type) {
		case string:
			parts = append(parts, pyLit(x))
		case json.Number:
			if strings.ContainsAny(x.String(), ".eE") {
				return pyType{}, false // Literal takes no floats
			}
			parts = append(parts, x.String())
		case bool:
			if x {
				parts = append(parts, "True")
			} else {
				parts = append(parts, "False")
			}
		case nil:
			nullable = true
		default:
			return pyType{}, false
		}
	}
	if len(parts) == 0 {
		return pyType{}, false
	}
	c.g.needLiteral = true
	return pyType{expr: "Literal[" + strings.Join(unique(parts), ", ") + "]", nullable: nullable || typeHasNull(s)}, true
}

func (c *pyConverter) single(s map[string]any, t, path, hint string) pyType {
	switch t {
	case "string":
		return pyType{expr: "str"}
	case "integer":
		return pyType{expr: "int"}
	case "number":
		return pyType{expr: "float"}
	case "boolean":
		return pyType{expr: "bool"}
	case "array":
		if pre, ok := s["prefixItems"].([]any); ok && len(pre) > 0 {
			parts := make([]string, len(pre))
			for i, p := range pre {
				parts[i] = c.typeOf(p, fmt.Sprintf("%s/prefixItems/%d", path, i), fmt.Sprintf("%sItem%d", hint, i+1)).render()
			}
			return pyType{expr: "tuple[" + strings.Join(parts, ", ") + "]"}
		}
		if it, ok := s["items"]; ok {
			return pyType{expr: "list[" + c.typeOf(it, path+"/items", hint+"Item").render() + "]"}
		}
		return pyType{expr: "list[Any]"}
	case "object":
		if props, _ := s["properties"].(map[string]any); len(props) > 0 {
			return pyType{expr: c.class(s, path, hint)}
		}
		if ap, ok := s["additionalProperties"].(map[string]any); ok {
			return pyType{expr: "dict[str, " + c.typeOf(ap, path+"/additionalProperties", hint+"Value").render() + "]"}
		}
		return pyType{expr: "dict[str, Any]"}
	}
	return pyType{expr: "Any", nullable: true}
}

func (c *pyConverter) ref(ref, path string) pyType {
	if !strings.HasPrefix(ref, "#") {
		c.g.warn("%s%s: $ref %q is not local; typed Any", c.where, path, ref)
		return pyType{expr: "Any", nullable: true}
	}
	target := resolvePointer(c.root, strings.TrimPrefix(ref, "#"))
	if target == nil {
		c.g.warn("%s%s: $ref %q doesn't resolve; typed Any", c.where, path, ref)
		return pyType{expr: "Any", nullable: true}
	}
	name, seen := c.refs[ref]
	if !seen {
		last := ref[strings.LastIndex(ref, "/")+1:]
		name = c.g.claim(c.prefix+pyIdent(last), c.where+"$ref "+ref)
		if ref == "#" {
			name = c.g.claim(c.prefix+"Self", c.where+"$ref #")
		}
		c.refs[ref] = name
	}
	m, isMap := target.(map[string]any)
	if isMap && isObject(m) {
		if !c.g.emitted[name] {
			c.g.emitted[name] = true
			c.emitClass(name, name, m, path, "", "")
		}
		return pyType{expr: name, nullable: typeHasNull(m)}
	}
	// a reference to anything else is that type under a name
	if !c.g.emitted[name] {
		c.g.emitted[name] = true
		inner := c.typeOf(target, path, name)
		c.g.emit(fmt.Sprintf("%s = %s", name, inner.render()))
		return pyType{expr: name}
	}
	return pyType{expr: name}
}

// class emits a TypedDict for an object schema and answers its name; converting the same schema twice with the
// same hint emits it once.
func (c *pyConverter) class(s map[string]any, path, hint string) string {
	name := c.g.claim(hint, c.where+path)
	if c.g.emitted[name] {
		return name
	}
	c.g.emitted[name] = true
	c.emitClass(name, name, s, path, "", "")
	return name
}

// emitClass writes the TypedDict of an object schema. base is "backd.Doc" for a document. hint names the types
// of its fields.
func (c *pyConverter) emitClass(name, hint string, s map[string]any, path, base, doc string) {
	props, required := propsOf(s)
	var fields []pyField
	for _, n := range slices.Sorted(keys(props)) {
		if base != "" && (n == "id" || n == "_meta" || n == "_expanded") {
			c.g.warn("%s%s: the property %q is the document's own; left out of %s", c.where, path, n, name)
			continue
		}
		d, _ := str(mapOf(props[n])["description"])
		fields = append(fields, pyField{key: n, typ: c.typeOf(props[n], path+"/properties/"+n, hint+pyIdent(n)).render(), required: required[n], doc: d})
	}
	if doc == "" {
		if d, _ := str(s["description"]); d != "" {
			doc = d
		}
	}
	c.g.typedDict(name, base, fields, doc)
}

// realm renders the whole file for a realm.
func (g *pyGen) realm(rl *registry.Realm, o Options, header string) string {
	g.rl = rl
	dbs := make([]string, 0, len(rl.Databases))
	for n := range rl.Databases {
		if o.Database == "" || n == o.Database {
			dbs = append(dbs, n)
		}
	}
	sort.Strings(dbs)
	prefix := pyIdent(rl.Name)

	collPrefix := map[string]string{}
	for _, dbName := range dbs {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			collPrefix[dbName+"/"+c.Name] = g.claim(pyIdent(dbName)+pyIdent(c.Name), "collection "+dbName+"/"+c.Name)
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
	for _, async := range []bool{false, true} {
		g.wrappers(&tail, rl, dbs, prefix, async)
	}

	imports := []string{"Any"}
	if g.needCast {
		imports = append(imports, "cast")
	}
	if g.needLiteral {
		imports = append(imports, "Literal")
	}
	imports = append(imports, "TypedDict")
	slices.Sort(imports)
	src := header + "from __future__ import annotations\n\nfrom typing import " + strings.Join(imports, ", ") + "\n\nimport backd\n\n\n" + strings.TrimRight(g.decls.String(), "\n") + "\n\n\n" + strings.TrimRight(tail.String(), "\n") + "\n"
	return src
}

// wrappers writes the typed accessors of a realm for the sync client or the async one.
func (g *pyGen) wrappers(w *strings.Builder, rl *registry.Realm, dbs []string, prefix string, async bool) {
	pre, client, db, coll, job, def, aw := "", "backd.Client", "backd.Database", "backd.Collection", "backd.Job", "def", ""
	if async {
		pre, client, db, coll, job, def, aw = "Async", "backd.AsyncClient", "backd.AsyncDatabase", "backd.AsyncCollection", "backd.AsyncJob", "async def", "await "
	}
	realmName := g.claim(pre+prefix+"Realm", "realm "+pre+rl.Name)
	dbTypes := map[string]string{}
	for _, dbName := range dbs {
		dbTypes[dbName] = g.claim(pre+prefix+pyIdent(dbName), "database "+pre+dbName)
	}
	for _, dbName := range dbs {
		database := rl.Databases[dbName]
		name := dbTypes[dbName]
		fmt.Fprintf(w, "class %s:\n    \"\"\"The %s database of %s, typed.\"\"\"\n\n    def __init__(self, db: %s) -> None:\n        self.db = db\n", name, pyLit(dbName), pyLit(rl.Name), db)
		members := map[string]bool{"db": true}
		member := func(base string) string {
			m := pySnake(base)
			for members[m] || pyKeywords[m] {
				m += "_"
			}
			members[m] = true
			return m
		}
		for _, c := range database.SortedCollections() {
			docT := g.collTypes[dbName+"/"+c.Name][0]
			m := member(c.Name)
			fmt.Fprintf(w, "\n    @property\n    def %s(self) -> %s[%s]:\n        \"\"\"The %s collection: documents read as %s.\"\"\"\n        return self.db.collection(%s, %s)\n", m, coll, docT, pyLit(c.Name), docT, pyLit(c.Name), docT)
		}
		if database.Functions != nil {
			for _, fn := range slices.Sorted(keys(database.Functions.Functions)) {
				f := database.Functions.Functions[fn]
				if f.Internal {
					continue
				}
				m := member(fn)
				ts := g.fnTypes[dbName+"/"+f.Name]
				inT, outT := ts[0], ts[1]
				param := "input: " + inT
				if inT == "" {
					param = "input: Any = None"
				}
				switch {
				case f.Mode == registry.ModeAsync:
					g.needCast = true
					fmt.Fprintf(w, "\n    %s %s(self, %s) -> %s:\n        \"\"\"Calls %s/%s (async: it answers a job; wait() for the output).\"\"\"\n        return cast(%s, %sself.db.fn(%s, input))\n", def, m, param, job, dbName, f.Name, job, aw, pyLit(f.Name))
				default:
					if outT == "" {
						outT = "Any"
					}
					g.needCast = true
					fmt.Fprintf(w, "\n    %s %s(self, %s) -> %s:\n        \"\"\"Calls %s/%s.\"\"\"\n        return cast(%s, %sself.db.fn(%s, input))\n", def, m, param, outT, dbName, f.Name, outT, aw, pyLit(f.Name))
				}
			}
		}
		w.WriteString("\n\n")
	}
	fmt.Fprintf(w, "class %s:\n    \"\"\"The %s realm bound to a client: its databases, collections and functions, typed.\"\"\"\n\n    def __init__(self, client: %s) -> None:\n        self.client = client\n", realmName, pyLit(rl.Name), client)
	members := map[string]bool{"client": true, "load_config": rl.AppConfig != nil}
	for _, dbName := range dbs {
		m := pySnake(dbName)
		for members[m] || pyKeywords[m] {
			m += "_"
		}
		members[m] = true
		fmt.Fprintf(w, "\n    @property\n    def %s(self) -> %s:\n        \"\"\"The %s database.\"\"\"\n        return %s(self.client.db(%s))\n", m, dbTypes[dbName], pyLit(dbName), dbTypes[dbName], pyLit(dbName))
	}
	if rl.AppConfig != nil {
		g.needCast = true
		fmt.Fprintf(w, "\n    %s load_config(self) -> %s:\n        \"\"\"Reads the realm's app configuration, as the caller sees it.\"\"\"\n        return cast(%s, %sself.client.config.load())\n", map[bool]string{false: "def", true: "async def"}[async], g.configTree, g.configTree, aw)
	}
	w.WriteString("\n\n")
}

// pySnake renders a name as a lower_snake Python attribute.
func pySnake(s string) string {
	c := pyConst(s)
	return strings.ToLower(c)
}

// collection emits one collection's types.
func (g *pyGen) collection(db string, c *registry.Collection, prefix string) {
	where0 := db + "/" + c.Name + " schema.json "
	docSchema, inputSchema := fileAware(c)
	conv := newPyConverter(docSchema, prefix, where0, g)
	inConv := newPyConverter(inputSchema, prefix, where0, g)

	docName := prefix
	inName := g.claim(prefix+"Input", "input "+db+"/"+c.Name)
	createName := ""
	g.collTypes[db+"/"+c.Name] = [3]string{docName, inName, createName}

	g.classOrDict(conv, docName, docSchema, "backd.Doc", fmt.Sprintf("A %s/%s document.", db, c.Name))
	g.classOrDict(inConv, inName, inputSchema, "", fmt.Sprintf("What a %s/%s write accepts.", db, c.Name))
	if c.CustomID != nil {
		createName = g.claim(prefix+"Create", "create "+db+"/"+c.Name)
		g.collTypes[db+"/"+c.Name] = [3]string{docName, inName, createName}
		note := "the id is chosen by the client and never changes; it must match " + c.CustomID.Anchored()
		if c.CustomID.User {
			note = "the id is chosen by the client and never changes; it must be the id of the signed-in user"
		}
		doc := fmt.Sprintf("What creating a %s/%s document takes: %s.", db, c.Name, note)
		if isObject(inputSchema) {
			g.emit(fmt.Sprintf("class %s(%s):\n%s    id: str", createName, inName, pyDoc("    ", doc)))
		} else {
			g.emit(fmt.Sprintf("%s = dict[str, Any]\n%s", createName, "\"\"\""+doc+" The document must carry \\\"id\\\".\"\"\""))
		}
	}
	g.queryConsts(db, c, prefix)
	if c.Aggregate != nil {
		g.aggregateConsts(c, prefix)
	}
}

// classOrDict emits an object schema as a TypedDict, or a schema with no declared properties as an alias (a plain
// document for a document, a dict for a write).
func (g *pyGen) classOrDict(conv *pyConverter, name string, schema map[string]any, base, doc string) {
	g.emitted[name] = true
	if !isObject(schema) {
		if base != "" {
			g.emit(fmt.Sprintf("class %s(%s):\n%s", name, base, pyDoc("    ", doc)))
			return
		}
		g.emit(fmt.Sprintf("%s = dict[str, Any]", name))
		return
	}
	conv.emitClass(name, conv.prefix, schema, "", base, doc)
}

// queryConsts emits the fields a where or an order_by of the collection may name.
func (g *pyGen) queryConsts(db string, c *registry.Collection, prefix string) {
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
	name := g.claim(prefix+"Fields", "fields "+db+"/"+c.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "class %s:\n    \"\"\"The fields the where and the order_by of %s/%s may name; the _DESC constants sort descending.\"\"\"\n\n", name, db, c.Name)
	used := map[string]bool{}
	for _, f := range fields {
		if f.kind == kindOpaque {
			continue
		}
		id := pyConst(f.path)
		for used[id] || used[id+"_DESC"] {
			id += "_X"
		}
		used[id], used[id+"_DESC"] = true, true
		fmt.Fprintf(&b, "    %s = %s\n    %s_DESC = %s\n", id, pyLit(f.path), id, pyLit("-"+f.path))
	}
	g.emit(strings.TrimRight(b.String(), "\n"))
}

// aggregateConsts emits what an aggregate of a collection may use.
func (g *pyGen) aggregateConsts(c *registry.Collection, prefix string) {
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
			parts[i] = pyLit(f)
		}
		if len(parts) == 1 {
			return "(" + parts[0] + ",)"
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	g.emit(fmt.Sprintf("class %s:\n    \"\"\"What an aggregate of %s may use: the group-by fields, those of them that are dates (they can be\n    bucketed \"field:day\"), and the metrics.\"\"\"\n\n    GROUP_BY: tuple[str, ...] = %s\n    DATES: tuple[str, ...] = %s\n    METRICS: tuple[str, ...] = %s", name, c.Name, list(a.GroupBy), list(dates), list(a.Metrics)))
}

// function emits a function's input and output types.
func (g *pyGen) function(db string, fn *registry.Function) {
	in, _ := readFunctionSchema(fn.Dir, registry.InputSchemaFile)
	out, _ := readFunctionSchema(fn.Dir, registry.OutputSchemaFile)
	prefix := "Fn" + pyIdent(db) + pyIdent(fn.Name)
	key := db + "/" + fn.Name
	var inT, outT string
	if in != nil {
		conv := newPyConverter(in, prefix, key+" "+registry.InputSchemaFile+" ", g)
		inT = g.claim(prefix+"Input", "function "+key+" input")
		g.typeDecl(conv, inT, in, "The input of "+key+".")
	}
	if out != nil {
		conv := newPyConverter(out, prefix, key+" "+registry.OutputSchemaFile+" ", g)
		outT = g.claim(prefix+"Output", "function "+key+" output")
		g.typeDecl(conv, outT, out, "The output of "+key+".")
	}
	g.fnTypes[key] = [2]string{inT, outT}
}

// typeDecl declares a named type for a schema: a TypedDict for an object, else an alias of its Python type.
func (g *pyGen) typeDecl(conv *pyConverter, name string, schema map[string]any, doc string) {
	if isObject(schema) {
		g.classOrDict(conv, name, schema, "", doc)
		return
	}
	g.emitted[name] = true
	t := conv.typeOf(schema, "", name)
	g.emit(fmt.Sprintf("%s = %s", name, t.render()))
}

// config emits the configuration's tree and the constants of its leaf paths.
func (g *pyGen) config(prefix string, ac *registry.AppConfig) {
	conv := newPyConverter(ac.RawSchema, prefix+"Config", "_realm/config/schema.json ", g)
	name := g.claim(prefix+"ConfigTree", "config tree")
	g.configTree = name
	g.typeDecl(conv, name, ac.RawSchema, "The realm's app configuration.")
	cn := g.claim(prefix+"ConfigPath", "config paths")
	var b strings.Builder
	fmt.Fprintf(&b, "class %s:\n    \"\"\"The app configuration's leaf paths, for client.config.get.\"\"\"\n\n", cn)
	used := map[string]bool{}
	for _, p := range slices.Sorted(keys(ac.Paths)) {
		if !ac.Paths[p].Leaf {
			continue
		}
		id := pyConst(p)
		for used[id] {
			id += "_X"
		}
		used[id] = true
		fmt.Fprintf(&b, "    %s = %s\n", id, pyLit(p))
	}
	g.emit(strings.TrimRight(b.String(), "\n"))
}

// roleConsts emits the realm's role, organization role, team role and kind names as constants.
func (g *pyGen) roleConsts(prefix string, rl *registry.Realm) {
	consts := func(name, what string, values []string) {
		if len(values) == 0 {
			return
		}
		cn := g.claim(prefix+name, what+" of "+rl.Name)
		var b strings.Builder
		fmt.Fprintf(&b, "class %s:\n    \"\"\"The %s %s declares.\"\"\"\n\n", cn, what, pyLit(rl.Name))
		used := map[string]bool{}
		for _, v := range values {
			id := pyConst(v)
			for used[id] {
				id += "_X"
			}
			used[id] = true
			fmt.Fprintf(&b, "    %s = %s\n", id, pyLit(v))
		}
		g.emit(strings.TrimRight(b.String(), "\n"))
	}
	consts("Roles", "roles", slices.Sorted(keys(rl.Settings.Roles)))
	if o := rl.Settings.Organizations; o != nil {
		consts("OrgRoles", "organization roles", o.RoleNames())
		consts("TeamRoles", "team roles", o.TeamRoleNames())
	}
	if rl.Email != nil {
		consts("EmailKinds", "email kinds", rl.Email.Kinds())
	}
	if rl.Push != nil {
		consts("PushKinds", "push kinds", rl.Push.Kinds())
	}
}
