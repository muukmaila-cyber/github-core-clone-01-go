package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- lexer

type gqlToken struct {
	kind string // name, int, float, string, punct, eof
	text string
}

func gqlLex(src string) ([]gqlToken, error) {
	var toks []gqlToken
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '{' || c == '}' || c == '(' || c == ')' || c == '[' || c == ']' || c == ':' || c == '!' || c == '=' || c == '@' || c == '$':
			toks = append(toks, gqlToken{kind: "punct", text: string(c)})
			i++
		case c == '.' && i+2 < len(src) && src[i+1] == '.' && src[i+2] == '.':
			toks = append(toks, gqlToken{kind: "punct", text: "..."})
			i += 3
		case c == '"':
			j := i + 1
			var sb strings.Builder
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' && j+1 < len(src) {
					switch src[j+1] {
					case 'n':
						sb.WriteByte('\n')
						j += 2
					case 't':
						sb.WriteByte('\t')
						j += 2
					case 'r':
						sb.WriteByte('\r')
						j += 2
					case '"':
						sb.WriteByte('"')
						j += 2
					case '\\':
						sb.WriteByte('\\')
						j += 2
					case '/':
						sb.WriteByte('/')
						j += 2
					case 'u':
						if j+5 < len(src) {
							if n, err := strconv.ParseUint(src[j+2:j+6], 16, 32); err == nil {
								sb.WriteRune(rune(n))
							}
							j += 6
						} else {
							j += 2
						}
					default:
						sb.WriteByte(src[j+1])
						j += 2
					}
					continue
				}
				sb.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string")
			}
			toks = append(toks, gqlToken{kind: "string", text: sb.String()})
			i = j + 1
		case (c >= '0' && c <= '9') || c == '-':
			j := i
			if src[j] == '-' {
				j++
			}
			isFloat := false
			for j < len(src) && ((src[j] >= '0' && src[j] <= '9') || src[j] == '.' || src[j] == 'e' || src[j] == 'E' || src[j] == '+' || src[j] == '-') {
				if src[j] == '.' || src[j] == 'e' || src[j] == 'E' {
					isFloat = true
				}
				j++
			}
			kind := "int"
			if isFloat {
				kind = "float"
			}
			toks = append(toks, gqlToken{kind: kind, text: src[i:j]})
			i = j
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_':
			j := i
			for j < len(src) && ((src[j] >= 'a' && src[j] <= 'z') || (src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= '0' && src[j] <= '9') || src[j] == '_') {
				j++
			}
			toks = append(toks, gqlToken{kind: "name", text: src[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	toks = append(toks, gqlToken{kind: "eof"})
	return toks, nil
}

// ---------------------------------------------------------------- AST

type gqlValue struct {
	kind    string // string, int, float, bool, null, enum, var, list, object
	str     string
	num     float64
	boolean bool
	list    []*gqlValue
	obj     map[string]*gqlValue
}

type gqlField struct {
	alias    string
	name     string
	args     map[string]*gqlValue
	sel      []*gqlField
	fragCond string // for inline fragments: the type condition (name "...")
}

type gqlOp struct {
	opType  string // query | mutation
	name    string
	varDefs map[string]*gqlValue // variable defaults
	sel     []*gqlField
}

type gqlParser struct {
	toks []gqlToken
	pos  int
}

func (p *gqlParser) peek() gqlToken  { return p.toks[p.pos] }
func (p *gqlParser) next() gqlToken  { t := p.toks[p.pos]; p.pos++; return t }
func (p *gqlParser) expect(text string) error {
	t := p.next()
	if t.text != text {
		return fmt.Errorf("expected %q, got %q", text, t.text)
	}
	return nil
}

func gqlParse(src string) ([]*gqlOp, error) {
	toks, err := gqlLex(src)
	if err != nil {
		return nil, err
	}
	p := &gqlParser{toks: toks}
	fragments := map[string]*gqlField{}
	var ops []*gqlOp
	for p.peek().kind != "eof" {
		t := p.peek()
		if t.kind == "name" && t.text == "fragment" {
			p.next()
			nameTok := p.next()
			if nameTok.kind != "name" {
				return nil, fmt.Errorf("fragment needs a name")
			}
			if err := p.expect("on"); err != nil {
				return nil, err
			}
			condTok := p.next()
			sel, err := p.parseSelection()
			if err != nil {
				return nil, err
			}
			fragments[nameTok.text] = &gqlField{name: "...", fragCond: condTok.text, sel: sel}
			continue
		}
		op, err := p.parseOperation()
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	// inline named fragment spreads
	var expand func(fields []*gqlField) []*gqlField
	expand = func(fields []*gqlField) []*gqlField {
		var out []*gqlField
		for _, f := range fields {
			if f.name == "..." && f.fragCond == "" && f.alias != "" {
				// named spread: alias holds the fragment name
				if frag, ok := fragments[f.alias]; ok {
					out = append(out, expand(frag.sel)...)
					continue
				}
			}
			f.sel = expand(f.sel)
			out = append(out, f)
		}
		return out
	}
	for _, op := range ops {
		op.sel = expand(op.sel)
	}
	return ops, nil
}

func (p *gqlParser) parseOperation() (*gqlOp, error) {
	op := &gqlOp{opType: "query", varDefs: map[string]*gqlValue{}}
	t := p.peek()
	if t.kind == "name" && (t.text == "query" || t.text == "mutation" || t.text == "subscription") {
		p.next()
		op.opType = t.text
		if p.peek().kind == "name" {
			op.name = p.next().text
		}
		if p.peek().text == "(" {
			if err := p.parseVarDefs(op); err != nil {
				return nil, err
			}
		}
	}
	sel, err := p.parseSelection()
	if err != nil {
		return nil, err
	}
	op.sel = sel
	return op, nil
}

func (p *gqlParser) parseVarDefs(op *gqlOp) error {
	if err := p.expect("("); err != nil {
		return err
	}
	for p.peek().text != ")" {
		if p.peek().text != "$" {
			return fmt.Errorf("expected $ in variable definition")
		}
		p.next()
		name := p.next()
		if err := p.expect(":"); err != nil {
			return err
		}
		// type: name, possibly [..] and ! — skip tokens
		for {
			t := p.next()
			if t.text == "$" {
				// next variable definition (commas are optional in GraphQL)
				p.pos--
				break
			}
			if t.text == "=" {
				v, err := p.parseValue()
				if err != nil {
					return err
				}
				op.varDefs[name.text] = v
				break
			}
			if t.text == "," || t.text == ")" {
				if t.text == ")" {
					// rewind one: the loop condition checks for ")"
					p.pos--
				}
				break
			}
			if t.kind == "eof" {
				return fmt.Errorf("unterminated variable definitions")
			}
		}
	}
	return p.expect(")")
}

func (p *gqlParser) parseSelection() ([]*gqlField, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	var fields []*gqlField
	for p.peek().text != "}" {
		if p.peek().kind == "eof" {
			return nil, fmt.Errorf("unterminated selection set")
		}
		f, err := p.parseField()
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	if err := p.expect("}"); err != nil {
		return nil, err
	}
	return fields, nil
}

func (p *gqlParser) parseField() (*gqlField, error) {
	t := p.peek()
	if t.text == "..." {
		p.next()
		if p.peek().kind == "name" && p.peek().text == "on" {
			p.next()
			cond := p.next()
			sel, err := p.parseSelection()
			if err != nil {
				return nil, err
			}
			return &gqlField{name: "...", fragCond: cond.text, sel: sel}, nil
		}
		if p.peek().kind == "name" {
			name := p.next()
			return &gqlField{name: "...", alias: name.text}, nil
		}
		return nil, fmt.Errorf("bad fragment spread")
	}
	nameTok := p.next()
	if nameTok.kind != "name" {
		return nil, fmt.Errorf("expected field name, got %q", nameTok.text)
	}
	f := &gqlField{name: nameTok.text, args: map[string]*gqlValue{}}
	if p.peek().text == ":" {
		p.next()
		realName := p.next()
		if realName.kind != "name" {
			return nil, fmt.Errorf("expected field name after alias")
		}
		f.alias = f.name
		f.name = realName.text
	}
	// directives: skip @name[(args)]
	for p.peek().text == "@" {
		p.next()
		p.next() // directive name
		if p.peek().text == "(" {
			if _, err := p.parseArgs(); err != nil {
				return nil, err
			}
		}
	}
	if p.peek().text == "(" {
		args, err := p.parseArgs()
		if err != nil {
			return nil, err
		}
		f.args = args
	}
	if p.peek().text == "{" {
		sel, err := p.parseSelection()
		if err != nil {
			return nil, err
		}
		f.sel = sel
	}
	return f, nil
}

func (p *gqlParser) parseArgs() (map[string]*gqlValue, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	args := map[string]*gqlValue{}
	for p.peek().text != ")" {
		name := p.next()
		if name.kind != "name" {
			return nil, fmt.Errorf("expected argument name")
		}
		if err := p.expect(":"); err != nil {
			return nil, err
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		args[name.text] = v
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	return args, nil
}

func (p *gqlParser) parseValue() (*gqlValue, error) {
	t := p.next()
	switch {
	case t.text == "$":
		name := p.next()
		return &gqlValue{kind: "var", str: name.text}, nil
	case t.kind == "string":
		return &gqlValue{kind: "string", str: t.text}, nil
	case t.kind == "int":
		n, _ := strconv.ParseFloat(t.text, 64)
		return &gqlValue{kind: "int", num: n, str: t.text}, nil
	case t.kind == "float":
		n, _ := strconv.ParseFloat(t.text, 64)
		return &gqlValue{kind: "float", num: n}, nil
	case t.kind == "name":
		switch t.text {
		case "true":
			return &gqlValue{kind: "bool", boolean: true}, nil
		case "false":
			return &gqlValue{kind: "bool", boolean: false}, nil
		case "null":
			return &gqlValue{kind: "null"}, nil
		default:
			return &gqlValue{kind: "enum", str: t.text}, nil
		}
	case t.text == "[":
		var list []*gqlValue
		for p.peek().text != "]" {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		if err := p.expect("]"); err != nil {
			return nil, err
		}
		return &gqlValue{kind: "list", list: list}, nil
	case t.text == "{":
		obj := map[string]*gqlValue{}
		for p.peek().text != "}" {
			name := p.next()
			if name.kind != "name" {
				return nil, fmt.Errorf("expected object field name")
			}
			if err := p.expect(":"); err != nil {
				return nil, err
			}
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			obj[name.text] = v
		}
		if err := p.expect("}"); err != nil {
			return nil, err
		}
		return &gqlValue{kind: "object", obj: obj}, nil
	}
	return nil, fmt.Errorf("unexpected value token %q", t.text)
}

// ---------------------------------------------------------------- value resolution

func resolveGQLValue(v *gqlValue, vars map[string]any) any {
	if v == nil {
		return nil
	}
	switch v.kind {
	case "string":
		return v.str
	case "int":
		return int64(v.num)
	case "float":
		return v.num
	case "bool":
		return v.boolean
	case "null":
		return nil
	case "enum":
		return v.str
	case "var":
		if val, ok := vars[v.str]; ok {
			return val
		}
		return nil
	case "list":
		out := make([]any, 0, len(v.list))
		for _, e := range v.list {
			out = append(out, resolveGQLValue(e, vars))
		}
		return out
	case "object":
		out := make(map[string]any, len(v.obj))
		for k, e := range v.obj {
			out[k] = resolveGQLValue(e, vars)
		}
		return out
	}
	return nil
}

func argString(args map[string]any, name string) string {
	if v, ok := args[name].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, name string) (int, bool) {
	switch v := args[name].(type) {
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

// ---------------------------------------------------------------- global ids

func encodeGID(parts ...string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(parts, ":")))
}

func decodeGID(s string) (string, []string, bool) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) < 2 {
		return "", nil, false
	}
	return parts[0], parts[1:], true
}

// ---------------------------------------------------------------- executor

type gqlNode struct {
	typ string
	obj any
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlExec struct {
	s      *Store
	vars   map[string]any
	errors []map[string]any
}

func (e *gqlExec) errf(format string, args ...any) {
	e.errors = append(e.errors, map[string]any{"message": fmt.Sprintf(format, args...)})
}

func (e *gqlExec) execute(op *gqlOp) map[string]any {
	rootType := "Query"
	if op.opType == "mutation" {
		rootType = "Mutation"
	}
	data := e.resolveSelection(rootType, nil, op.sel)
	out := map[string]any{"data": data}
	if len(e.errors) > 0 {
		out["errors"] = e.errors
	}
	return out
}

// resolveSelection resolves a selection set against an object of type typ.
func (e *gqlExec) resolveSelection(typ string, obj any, sel []*gqlField) map[string]any {
	out := map[string]any{}
	for _, f := range sel {
		if f.name == "..." {
			// inline fragment: apply when the type matches (or no condition)
			if f.fragCond == "" || f.fragCond == typ {
				sub := e.resolveSelection(typ, obj, f.sel)
				for k, v := range sub {
					out[k] = v
				}
			}
			continue
		}
		key := f.name
		if f.alias != "" {
			key = f.alias
		}
		val := e.resolveField(typ, obj, f)
		out[key] = val
	}
	return out
}

// resolveObject resolves a value that is a gqlNode or list of them into JSON.
func (e *gqlExec) resolveObject(val any, f *gqlField) any {
	switch v := val.(type) {
	case *gqlNode:
		if v == nil {
			return nil
		}
		return e.resolveSelection(v.typ, v.obj, f.sel)
	case []*gqlNode:
		out := make([]any, 0, len(v))
		for _, n := range v {
			out = append(out, e.resolveObject(n, f))
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, e.resolveObject(item, f))
		}
		return out
	default:
		return val
	}
}

func (e *gqlExec) argsOf(f *gqlField) map[string]any {
	args := make(map[string]any, len(f.args))
	for k, v := range f.args {
		args[k] = resolveGQLValue(v, e.vars)
	}
	return args
}

func (e *gqlExec) resolveField(typ string, obj any, f *gqlField) any {
	args := e.argsOf(f)
	switch f.name {
	case "__typename":
		return typ
	case "__type":
		name := argString(args, "name")
		t, ok := gqlTypeTable[name]
		if !ok {
			return nil
		}
		return e.resolveObject(&gqlNode{typ: "__Type", obj: &gqlTypeInfo{name: name, kind: t.kind, fields: t.fields, enumValues: t.enumValues}}, f)
	case "__schema":
		return e.resolveObject(&gqlNode{typ: "__Schema", obj: &gqlTypeInfo{name: "__Schema"}}, f)
	}

	switch typ {
	case "Query":
		return e.resolveQuery(obj, f, args)
	case "Mutation":
		return e.resolveMutation(obj, f, args)
	case "Repository":
		return e.resolveRepository(obj.(*Repo), f, args)
	case "Issue":
		return e.resolveIssue(obj.(*Issue), f, args)
	case "PullRequest":
		return e.resolvePull(obj.(*Pull), f, args)
	case "User":
		u := obj.(*User)
		switch f.name {
		case "id":
			return encodeGID("User", strconv.FormatInt(u.ID, 10))
		case "login":
			return u.Login
		case "name":
			return u.Name
		case "email":
			return u.Email
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "Ref":
		r := obj.(*refNode)
		switch f.name {
		case "name":
			return r.name
		case "prefix":
			return "refs/heads/"
		case "target":
			return nil
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "IssueConnection":
		c := obj.(*issueConn)
		switch f.name {
		case "nodes":
			nodes := make([]*gqlNode, 0, len(c.items))
			for _, is := range c.items {
				nodes = append(nodes, &gqlNode{typ: "Issue", obj: is})
			}
			return e.resolveObject(nodes, f)
		case "edges":
			edges := make([]*gqlNode, 0, len(c.items))
			for i, is := range c.items {
				edges = append(edges, &gqlNode{typ: "IssueEdge", obj: &edgeNode{node: &gqlNode{typ: "Issue", obj: is}, cursor: c.cursorAt(i)}})
			}
			return e.resolveObject(edges, f)
		case "pageInfo":
			return e.resolveObject(&gqlNode{typ: "PageInfo", obj: &pageInfo{hasNext: c.hasNext, endCursor: c.endCursor}}, f)
		case "totalCount":
			return c.total
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "PullRequestConnection":
		c := obj.(*pullConn)
		switch f.name {
		case "nodes":
			nodes := make([]*gqlNode, 0, len(c.items))
			for _, pr := range c.items {
				nodes = append(nodes, &gqlNode{typ: "PullRequest", obj: pr})
			}
			return e.resolveObject(nodes, f)
		case "edges":
			edges := make([]*gqlNode, 0, len(c.items))
			for i, pr := range c.items {
				edges = append(edges, &gqlNode{typ: "PullRequestEdge", obj: &edgeNode{node: &gqlNode{typ: "PullRequest", obj: pr}, cursor: c.cursorAt(i)}})
			}
			return e.resolveObject(edges, f)
		case "pageInfo":
			return e.resolveObject(&gqlNode{typ: "PageInfo", obj: &pageInfo{hasNext: c.hasNext, endCursor: c.endCursor}}, f)
		case "totalCount":
			return c.total
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "IssueEdge", "PullRequestEdge":
		ed := obj.(*edgeNode)
		switch f.name {
		case "node":
			return e.resolveObject(ed.node, f)
		case "cursor":
			return ed.cursor
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "PageInfo":
		pi := obj.(*pageInfo)
		switch f.name {
		case "hasNextPage":
			return pi.hasNext
		case "endCursor":
			return pi.endCursor
		case "hasPreviousPage":
			return false
		case "startCursor":
			return nil
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "Comment":
		c := obj.(*Comment)
		switch f.name {
		case "id":
			return encodeGID("Comment", strconv.FormatInt(c.ID, 10))
		case "body":
			return c.Body
		case "author":
			return e.resolveObject(&gqlNode{typ: "User", obj: c.User}, f)
		case "createdAt":
			return c.CreatedAt
		case "updatedAt":
			return c.CreatedAt
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "CommentEdge":
		ed := obj.(*edgeNode)
		switch f.name {
		case "node":
			return e.resolveObject(ed.node, f)
		case "cursor":
			return ed.cursor
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "SearchResult":
		switch f.name {
		case "nodes":
			return []any{}
		case "edges":
			return []any{}
		case "issueCount":
			return 0
		case "pageInfo":
			return e.resolveObject(&gqlNode{typ: "PageInfo", obj: &pageInfo{hasNext: false, endCursor: nil}}, f)
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "Label":
		l := obj.(*Label)
		switch f.name {
		case "name":
			return l.Name
		case "color":
			return l.Color
		case "description":
			return l.Description
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "LabelConnection":
		c := obj.(*labelConn)
		switch f.name {
		case "nodes":
			nodes := make([]*gqlNode, 0, len(c.items))
			for _, l := range c.items {
				nodes = append(nodes, &gqlNode{typ: "Label", obj: l})
			}
			return e.resolveObject(nodes, f)
		case "totalCount":
			return len(c.items)
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "CommentConnection":
		c := obj.(*commentConn)
		switch f.name {
		case "nodes":
			nodes := make([]*gqlNode, 0, len(c.items))
			for _, cm := range c.items {
				nodes = append(nodes, &gqlNode{typ: "Comment", obj: cm})
			}
			return e.resolveObject(nodes, f)
		case "edges":
			edges := make([]*gqlNode, 0, len(c.items))
			for i, cm := range c.items {
				edges = append(edges, &gqlNode{typ: "CommentEdge", obj: &edgeNode{node: &gqlNode{typ: "Comment", obj: cm}, cursor: c.cursorAt(i)}})
			}
			return e.resolveObject(edges, f)
		case "pageInfo":
			return e.resolveObject(&gqlNode{typ: "PageInfo", obj: &pageInfo{hasNext: c.hasNext, endCursor: c.endCursor}}, f)
		case "totalCount":
			return c.total
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "__Type":
		ti := obj.(*gqlTypeInfo)
		switch f.name {
		case "name":
			return ti.name
		case "kind":
			return ti.kind
		case "description":
			return nil
		case "fields":
			if ti.kind != "OBJECT" {
				return nil
			}
			fields := make([]*gqlNode, 0, len(ti.fields))
			for _, name := range ti.fields {
				fields = append(fields, &gqlNode{typ: "__Field", obj: name})
			}
			return e.resolveObject(fields, f)
		case "enumValues":
			if ti.kind != "ENUM" {
				return nil
			}
			vals := make([]*gqlNode, 0, len(ti.enumValues))
			for _, name := range ti.enumValues {
				vals = append(vals, &gqlNode{typ: "__EnumValue", obj: name})
			}
			return e.resolveObject(vals, f)
		default:
			e.errf("Cannot query field %q on type %q", f.name, typ)
			return nil
		}
	case "__Field":
		switch f.name {
		case "name":
			return obj.(string)
		default:
			return nil
		}
	case "__EnumValue":
		switch f.name {
		case "name":
			return obj.(string)
		default:
			return nil
		}
	case "__Schema":
		switch f.name {
		case "queryType":
			return e.resolveObject(&gqlNode{typ: "__Type", obj: &gqlTypeInfo{name: "Query", kind: "OBJECT", fields: gqlTypeTable["Query"].fields}}, f)
		case "mutationType":
			return e.resolveObject(&gqlNode{typ: "__Type", obj: &gqlTypeInfo{name: "Mutation", kind: "OBJECT", fields: gqlTypeTable["Mutation"].fields}}, f)
		case "types":
			var types []*gqlNode
			names := make([]string, 0, len(gqlTypeTable))
			for n := range gqlTypeTable {
				names = append(names, n)
			}
			for _, n := range names {
				t := gqlTypeTable[n]
				types = append(types, &gqlNode{typ: "__Type", obj: &gqlTypeInfo{name: n, kind: t.kind, fields: t.fields, enumValues: t.enumValues}})
			}
			return e.resolveObject(types, f)
		default:
			return nil
		}
	}
	if strings.HasPrefix(f.name, "create") || strings.HasPrefix(f.name, "close") ||
		strings.HasPrefix(f.name, "merge") || strings.HasPrefix(f.name, "add") {
		// payload objects: resolve their fields from the stored map
		if m, ok := obj.(map[string]any); ok {
			if v, ok := m[f.name]; ok {
				return e.resolveObject(v, f)
			}
			// payload field names are the inner object keys
			if v, ok := m["__"+f.name]; ok {
				return e.resolveObject(v, f)
			}
		}
	}
	e.errf("Cannot query field %q on type %q", f.name, typ)
	return nil
}

// ---------------------------------------------------------------- connection helpers

type pageInfo struct {
	hasNext   bool
	endCursor any
}

type edgeNode struct {
	node   *gqlNode
	cursor string
}

type refNode struct {
	name string
}

type issueConn struct {
	items     []*Issue
	total     int
	hasNext   bool
	endCursor any
	base      int // index of the first item in the filtered list
}

func (c *issueConn) cursorAt(i int) string {
	return encodeGID("cursor", strconv.Itoa(c.base+i))
}

type pullConn struct {
	items     []*Pull
	total     int
	hasNext   bool
	endCursor any
	base      int
}

func (c *pullConn) cursorAt(i int) string {
	return encodeGID("cursor", strconv.Itoa(c.base+i))
}

type labelConn struct {
	items []*Label
}

type commentConn struct {
	items     []*Comment
	total     int
	hasNext   bool
	endCursor any
	base      int
}

func (c *commentConn) cursorAt(i int) string {
	return encodeGID("cursor", strconv.Itoa(c.base+i))
}

func decodeCursor(s string) int {
	_, parts, ok := decodeGID(s)
	if !ok || len(parts) == 0 {
		return -1
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return -1
	}
	return n
}

// ---------------------------------------------------------------- root resolvers

func (e *gqlExec) resolveQuery(obj any, f *gqlField, args map[string]any) any {
	switch f.name {
	case "repository":
		owner := argString(args, "owner")
		name := argString(args, "name")
		repo := e.s.getRepo(owner, name)
		if repo == nil {
			return nil
		}
		return e.resolveObject(&gqlNode{typ: "Repository", obj: repo}, f)
	case "node":
		id := argString(args, "id")
		kind, parts, ok := decodeGID(id)
		if !ok {
			return nil
		}
		switch kind {
		case "Repository":
			if len(parts) < 1 {
				return nil
			}
			idNum, _ := strconv.ParseInt(parts[0], 10, 64)
			for _, r := range e.s.viewerRepos() {
				if r.ID == idNum {
					return e.resolveObject(&gqlNode{typ: "Repository", obj: r}, f)
				}
			}
			return nil
		case "Issue":
			if len(parts) < 2 {
				return nil
			}
			repo := e.repoByID(parts[0])
			if repo == nil {
				return nil
			}
			num, _ := strconv.Atoi(parts[1])
			is := repo.IssueByNum[num]
			if is == nil {
				return nil
			}
			return e.resolveObject(&gqlNode{typ: "Issue", obj: is}, f)
		case "PullRequest":
			if len(parts) < 2 {
				return nil
			}
			repo := e.repoByID(parts[0])
			if repo == nil {
				return nil
			}
			num, _ := strconv.Atoi(parts[1])
			pr := repo.PullByNum[num]
			if pr == nil {
				return nil
			}
			return e.resolveObject(&gqlNode{typ: "PullRequest", obj: pr}, f)
		case "User":
			return e.resolveObject(&gqlNode{typ: "User", obj: e.s.viewer}, f)
		}
		return nil
	case "viewer":
		return e.resolveObject(&gqlNode{typ: "User", obj: e.s.viewer}, f)
	case "search":
		return e.resolveObject(&gqlNode{typ: "SearchResult", obj: &searchResult{}}, f)
	default:
		e.errf("Cannot query field %q on type %q", f.name, "Query")
		return nil
	}
}

type searchResult struct{}

func (e *gqlExec) repoByID(idStr string) *Repo {
	idNum, _ := strconv.ParseInt(idStr, 10, 64)
	for _, r := range e.s.viewerRepos() {
		if r.ID == idNum {
			return r
		}
	}
	return nil
}

func (e *gqlExec) resolveRepository(repo *Repo, f *gqlField, args map[string]any) any {
	switch f.name {
	case "id":
		return encodeGID("Repository", strconv.FormatInt(repo.ID, 10))
	case "databaseId":
		return repo.ID
	case "name":
		return repo.Name
	case "nameWithOwner":
		return repo.Owner + "/" + repo.Name
	case "owner":
		return e.resolveObject(&gqlNode{typ: "User", obj: e.s.viewer}, f)
	case "defaultBranchRef":
		if _, ok := repo.Refs["refs/heads/"+repo.DefaultBranch]; !ok {
			return nil
		}
		return e.resolveObject(&gqlNode{typ: "Ref", obj: &refNode{name: repo.DefaultBranch}}, f)
	case "viewerPermission":
		return "ADMIN"
	case "hasIssuesEnabled":
		return true
	case "description":
		return repo.Description
	case "private":
		return repo.Private
	case "url":
		return ""
	case "createdAt":
		return repo.CreatedAt
	case "updatedAt":
		return repo.UpdatedAt
	case "issue":
		num, ok := argInt(args, "number")
		if !ok {
			return nil
		}
		is := repo.IssueByNum[num]
		if is == nil {
			return nil
		}
		return e.resolveObject(&gqlNode{typ: "Issue", obj: is}, f)
	case "issues":
		return e.resolveObject(e.issueConnection(repo, args), f)
	case "pullRequest":
		num, ok := argInt(args, "number")
		if !ok {
			return nil
		}
		pr := repo.PullByNum[num]
		if pr == nil {
			return nil
		}
		return e.resolveObject(&gqlNode{typ: "PullRequest", obj: pr}, f)
	case "pullRequests":
		return e.resolveObject(e.pullConnection(repo, args), f)
	default:
		e.errf("Cannot query field %q on type %q", f.name, "Repository")
		return nil
	}
}

func (e *gqlExec) issueConnection(repo *Repo, args map[string]any) *gqlNode {
	// states filter
	states := map[string]bool{}
	if raw, ok := args["states"].([]any); ok {
		for _, s := range raw {
			if str, ok := s.(string); ok {
				states[str] = true
			}
		}
	}
	var filtered []*Issue
	for _, is := range repo.Issues {
		gqlState := "OPEN"
		if is.State == "closed" {
			gqlState = "CLOSED"
		}
		if len(states) > 0 && !states[gqlState] {
			continue
		}
		filtered = append(filtered, is)
	}
	// orderBy
	desc := false
	if ob, ok := args["orderBy"].(map[string]any); ok {
		if d, ok := ob["direction"].(string); ok && d == "DESC" {
			desc = true
		}
	}
	if desc {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}
	// after cursor
	start := 0
	if after, ok := args["after"].(string); ok && after != "" {
		if idx := decodeCursor(after); idx >= 0 {
			start = idx + 1
		}
	}
	first := len(filtered) - start
	if n, ok := argInt(args, "first"); ok && n < first {
		first = n
	}
	if first < 0 {
		first = 0
	}
	end := start + first
	hasNext := end < len(filtered)
	var endCursor any
	if first > 0 {
		endCursor = encodeGID("cursor", strconv.Itoa(end-1))
	}
	return &gqlNode{typ: "IssueConnection", obj: &issueConn{
		items: filtered[start:end], total: len(filtered),
		hasNext: hasNext, endCursor: endCursor, base: start,
	}}
}

func (e *gqlExec) pullConnection(repo *Repo, args map[string]any) *gqlNode {
	states := map[string]bool{}
	if raw, ok := args["states"].([]any); ok {
		for _, s := range raw {
			if str, ok := s.(string); ok {
				states[str] = true
			}
		}
	}
	var filtered []*Pull
	for _, pr := range repo.Pulls {
		gqlState := "OPEN"
		if pr.Merged {
			gqlState = "MERGED"
		} else if pr.State == "closed" {
			gqlState = "CLOSED"
		}
		if len(states) > 0 && !states[gqlState] {
			continue
		}
		filtered = append(filtered, pr)
	}
	desc := false
	if ob, ok := args["orderBy"].(map[string]any); ok {
		if d, ok := ob["direction"].(string); ok && d == "DESC" {
			desc = true
		}
	}
	if desc {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}
	start := 0
	if after, ok := args["after"].(string); ok && after != "" {
		if idx := decodeCursor(after); idx >= 0 {
			start = idx + 1
		}
	}
	first := len(filtered) - start
	if n, ok := argInt(args, "first"); ok && n < first {
		first = n
	}
	if first < 0 {
		first = 0
	}
	end := start + first
	hasNext := end < len(filtered)
	var endCursor any
	if first > 0 {
		endCursor = encodeGID("cursor", strconv.Itoa(end-1))
	}
	return &gqlNode{typ: "PullRequestConnection", obj: &pullConn{
		items: filtered[start:end], total: len(filtered),
		hasNext: hasNext, endCursor: endCursor, base: start,
	}}
}

func (e *gqlExec) resolveIssue(is *Issue, f *gqlField, args map[string]any) any {
	repo := is.repo
	switch f.name {
	case "id":
		return encodeGID("Issue", strconv.FormatInt(repo.ID, 10), strconv.Itoa(is.Number))
	case "number":
		return is.Number
	case "title":
		return is.Title
	case "state":
		if is.State == "closed" {
			return "CLOSED"
		}
		return "OPEN"
	case "body":
		return is.Body
	case "createdAt":
		return is.CreatedAt
	case "updatedAt":
		return is.UpdatedAt
	case "author":
		return e.resolveObject(&gqlNode{typ: "User", obj: e.s.viewer}, f)
	case "labels":
		return e.resolveObject(&gqlNode{typ: "LabelConnection", obj: &labelConn{items: is.Labels}}, f)
	case "comments":
		return e.resolveObject(&gqlNode{typ: "CommentConnection", obj: &commentConn{items: is.Comments, total: len(is.Comments)}}, f)
	case "repository":
		return e.resolveObject(&gqlNode{typ: "Repository", obj: repo}, f)
	case "url":
		return ""
	default:
		e.errf("Cannot query field %q on type %q", f.name, "Issue")
		return nil
	}
}

func (e *gqlExec) resolvePull(pr *Pull, f *gqlField, args map[string]any) any {
	repo := pr.repo
	switch f.name {
	case "id":
		return encodeGID("PullRequest", strconv.FormatInt(repo.ID, 10), strconv.Itoa(pr.Number))
	case "number":
		return pr.Number
	case "title":
		return pr.Title
	case "body":
		return pr.Body
	case "state":
		if pr.Merged {
			return "MERGED"
		}
		if pr.State == "closed" {
			return "CLOSED"
		}
		return "OPEN"
	case "headRefName":
		return pr.HeadRef
	case "baseRefName":
		return pr.BaseRef
	case "mergeable":
		return repo.mergeable(pr)
	case "merged":
		return pr.Merged
	case "createdAt":
		return pr.CreatedAt
	case "updatedAt":
		return pr.UpdatedAt
	case "author":
		return e.resolveObject(&gqlNode{typ: "User", obj: e.s.viewer}, f)
	case "files":
		files := repo.pullFiles(pr)
		// shape: {nodes:[{path, additions, deletions, changes}], totalCount}
		nodes := make([]any, 0, len(files))
		for _, fl := range files {
			nodes = append(nodes, map[string]any{
				"path": fl["filename"], "additions": fl["additions"],
				"deletions": fl["deletions"], "changes": fl["changes"],
			})
		}
		return map[string]any{"nodes": nodes, "totalCount": len(files)}
	default:
		e.errf("Cannot query field %q on type %q", f.name, "PullRequest")
		return nil
	}
}

// ---------------------------------------------------------------- mutations

func (e *gqlExec) inputMap(args map[string]any, name string) map[string]any {
	if m, ok := args[name].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func (e *gqlExec) resolveMutation(obj any, f *gqlField, args map[string]any) any {
	switch f.name {
	case "createIssue":
		in := e.inputMap(args, "input")
		kind, parts, ok := decodeGID(argString(in, "repositoryId"))
		if !ok || kind != "Repository" {
			e.errf("createIssue: unknown repository")
			return nil
		}
		repo := e.repoByID(parts[0])
		if repo == nil {
			e.errf("createIssue: unknown repository")
			return nil
		}
		title := argString(in, "title")
		body := argString(in, "body")
		_, now := e.s.tick()
		repo.IssueSeq++
		is := &Issue{
			ID: e.s.nextID(), Number: repo.IssueSeq, Title: title, Body: body,
			State: "open", Labels: []*Label{}, CreatedAt: now, UpdatedAt: now, repo: repo,
		}
		repo.Issues = append(repo.Issues, is)
		repo.IssueByNum[is.Number] = is
		return e.payload(f, map[string]any{
			"issue":           &gqlNode{typ: "Issue", obj: is},
			"clientMutationId": in["clientMutationId"],
		})
	case "closeIssue":
		in := e.inputMap(args, "input")
		kind, parts, ok := decodeGID(argString(in, "issueId"))
		if !ok || kind != "Issue" || len(parts) < 2 {
			e.errf("closeIssue: unknown issue")
			return nil
		}
		repo := e.repoByID(parts[0])
		if repo == nil {
			e.errf("closeIssue: unknown issue")
			return nil
		}
		num, _ := strconv.Atoi(parts[1])
		is := repo.IssueByNum[num]
		if is == nil {
			e.errf("closeIssue: unknown issue")
			return nil
		}
		is.State = "closed"
		_, now := e.s.tick()
		is.UpdatedAt = now
		return e.payload(f, map[string]any{
			"issue":           &gqlNode{typ: "Issue", obj: is},
			"clientMutationId": in["clientMutationId"],
		})
	case "createPullRequest":
		in := e.inputMap(args, "input")
		kind, parts, ok := decodeGID(argString(in, "repositoryId"))
		if !ok || kind != "Repository" {
			e.errf("createPullRequest: unknown repository")
			return nil
		}
		repo := e.repoByID(parts[0])
		if repo == nil {
			e.errf("createPullRequest: unknown repository")
			return nil
		}
		head := argString(in, "headRefName")
		base := argString(in, "baseRefName")
		headSHA, okH := repo.Refs["refs/heads/"+head]
		baseSHA, okB := repo.Refs["refs/heads/"+base]
		if !okH || !okB {
			e.errf("createPullRequest: head or base branch does not exist")
			return nil
		}
		_, now := e.s.tick()
		repo.PullSeq++
		pr := &Pull{
			ID: e.s.nextID(), Number: repo.PullSeq, Title: argString(in, "title"), Body: argString(in, "body"),
			HeadRef: head, BaseRef: base, HeadSHA: headSHA, BaseSHA: baseSHA,
			State: "open", CreatedAt: now, UpdatedAt: now, repo: repo,
		}
		repo.Pulls = append(repo.Pulls, pr)
		repo.PullByNum[pr.Number] = pr
		return e.payload(f, map[string]any{
			"pullRequest":     &gqlNode{typ: "PullRequest", obj: pr},
			"clientMutationId": in["clientMutationId"],
		})
	case "mergePullRequest":
		in := e.inputMap(args, "input")
		kind, parts, ok := decodeGID(argString(in, "pullRequestId"))
		if !ok || kind != "PullRequest" || len(parts) < 2 {
			e.errf("mergePullRequest: unknown pull request")
			return nil
		}
		repo := e.repoByID(parts[0])
		if repo == nil {
			e.errf("mergePullRequest: unknown pull request")
			return nil
		}
		num, _ := strconv.Atoi(parts[1])
		pr := repo.PullByNum[num]
		if pr == nil {
			e.errf("mergePullRequest: unknown pull request")
			return nil
		}
		if pr.Merged {
			e.errf("mergePullRequest: already merged")
			return nil
		}
		if _, ok := e.s.mergePull(repo, pr); !ok {
			e.errf("mergePullRequest: merge conflict")
			return nil
		}
		return e.payload(f, map[string]any{
			"pullRequest":     &gqlNode{typ: "PullRequest", obj: pr},
			"clientMutationId": in["clientMutationId"],
		})
	case "addComment":
		in := e.inputMap(args, "input")
		kind, parts, ok := decodeGID(argString(in, "subjectId"))
		if !ok || kind != "Issue" || len(parts) < 2 {
			e.errf("addComment: unknown subject")
			return nil
		}
		repo := e.repoByID(parts[0])
		if repo == nil {
			e.errf("addComment: unknown subject")
			return nil
		}
		num, _ := strconv.Atoi(parts[1])
		is := repo.IssueByNum[num]
		if is == nil {
			e.errf("addComment: unknown subject")
			return nil
		}
		_, now := e.s.tick()
		c := &Comment{ID: e.s.nextID(), Body: argString(in, "body"), CreatedAt: now, User: e.s.viewer}
		is.Comments = append(is.Comments, c)
		edge := &gqlNode{typ: "CommentEdge", obj: &edgeNode{node: &gqlNode{typ: "Comment", obj: c}, cursor: encodeGID("cursor", "0")}}
		return e.payload(f, map[string]any{
			"commentEdge":     edge,
			"subject":         &gqlNode{typ: "Issue", obj: is},
			"clientMutationId": in["clientMutationId"],
		})
	default:
		e.errf("Cannot query field %q on type %q", f.name, "Mutation")
		return nil
	}
}

// payload wraps a mutation result so its fields resolve against the map.
func (e *gqlExec) payload(f *gqlField, m map[string]any) any {
	out := map[string]any{}
	for _, sub := range f.sel {
		if sub.name == "..." {
			if sub.fragCond == "" {
				for _, inner := range sub.sel {
					out[innerAlias(inner)] = e.resolvePayloadField(m, inner)
				}
			}
			continue
		}
		out[innerAlias(sub)] = e.resolvePayloadField(m, sub)
	}
	return out
}

func innerAlias(f *gqlField) string {
	if f.alias != "" {
		return f.alias
	}
	return f.name
}

func (e *gqlExec) resolvePayloadField(m map[string]any, f *gqlField) any {
	if f.name == "__typename" {
		return "Payload"
	}
	if f.name == "clientMutationId" {
		if v, ok := m["clientMutationId"]; ok {
			return v
		}
		return nil
	}
	if v, ok := m[f.name]; ok {
		return e.resolveObject(v, f)
	}
	e.errf("Cannot query field %q on mutation payload", f.name)
	return nil
}

// ---------------------------------------------------------------- introspection table

type gqlTypeDef struct {
	kind       string
	fields     []string
	enumValues []string
}

type gqlTypeInfo struct {
	name       string
	kind       string
	fields     []string
	enumValues []string
}

var gqlTypeTable = map[string]gqlTypeDef{
	"Query": {kind: "OBJECT", fields: []string{"repository", "node", "viewer", "search"}},
	"Mutation": {kind: "OBJECT", fields: []string{"createIssue", "closeIssue", "createPullRequest", "mergePullRequest", "addComment"}},
	"Repository": {kind: "OBJECT", fields: []string{"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequest", "pullRequests", "description", "private", "url", "createdAt", "updatedAt"}},
	"Issue": {kind: "OBJECT", fields: []string{"id", "number", "title", "state", "body", "createdAt", "updatedAt", "author", "labels", "comments", "repository", "url"}},
	"PullRequest": {kind: "OBJECT", fields: []string{"id", "number", "title", "body", "state", "headRefName", "baseRefName", "mergeable", "merged", "files", "createdAt", "updatedAt", "author"}},
	"User": {kind: "OBJECT", fields: []string{"id", "login", "name", "email"}},
	"Ref": {kind: "OBJECT", fields: []string{"name", "prefix", "target"}},
	"IssueConnection": {kind: "OBJECT", fields: []string{"nodes", "edges", "pageInfo", "totalCount"}},
	"IssueEdge": {kind: "OBJECT", fields: []string{"node", "cursor"}},
	"PullRequestConnection": {kind: "OBJECT", fields: []string{"nodes", "edges", "pageInfo", "totalCount"}},
	"PullRequestEdge": {kind: "OBJECT", fields: []string{"node", "cursor"}},
	"PageInfo": {kind: "OBJECT", fields: []string{"hasNextPage", "endCursor", "hasPreviousPage", "startCursor"}},
	"Comment": {kind: "OBJECT", fields: []string{"id", "body", "author", "createdAt", "updatedAt"}},
	"CommentEdge": {kind: "OBJECT", fields: []string{"node", "cursor"}},
	"CommentConnection": {kind: "OBJECT", fields: []string{"nodes", "edges", "pageInfo", "totalCount"}},
	"Label": {kind: "OBJECT", fields: []string{"name", "color", "description"}},
	"LabelConnection": {kind: "OBJECT", fields: []string{"nodes", "totalCount"}},
	"IssueState": {kind: "ENUM", enumValues: []string{"OPEN", "CLOSED"}},
	"PullRequestState": {kind: "ENUM", enumValues: []string{"OPEN", "CLOSED", "MERGED"}},
	"MergeableState": {kind: "ENUM", enumValues: []string{"MERGEABLE", "CONFLICTING", "UNKNOWN"}},
	"SearchType": {kind: "ENUM", enumValues: []string{"ISSUE", "REPOSITORY", "USER", "DISCUSSION", "COMMIT", "CODE"}},
	"RepositoryPermission": {kind: "ENUM", enumValues: []string{"ADMIN", "MAINTAIN", "WRITE", "TRIAGE", "READ"}},
	"OrderDirection": {kind: "ENUM", enumValues: []string{"ASC", "DESC"}},
	"IssueOrderField": {kind: "ENUM", enumValues: []string{"CREATED_AT", "UPDATED_AT", "COMMENTS"}},
	"SearchResult": {kind: "OBJECT", fields: []string{"nodes", "edges", "issueCount", "pageInfo"}},
}

// ---------------------------------------------------------------- HTTP entry

func (s *Store) graphqlHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"errors": []map[string]string{{"message": "method not allowed"}}}, s)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body := readBody(r)
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": []map[string]string{{"message": "invalid JSON"}}}, s)
		return
	}
	// Batched operations: a JSON array is answered with an array, in order.
	if arr, ok := raw.([]any); ok {
		results := make([]any, 0, len(arr))
		for _, item := range arr {
			results = append(results, s.runGraphQL(item))
		}
		writeJSON(w, http.StatusOK, results, s)
		return
	}
	writeJSON(w, http.StatusOK, s.runGraphQL(raw), s)
}

func (s *Store) runGraphQL(raw any) map[string]any {
	m, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{"errors": []map[string]string{{"message": "expected a JSON object"}}}
	}
	query, _ := m["query"].(string)
	opName, _ := m["operationName"].(string)
	vars := map[string]any{}
	if v, ok := m["variables"].(map[string]any); ok {
		vars = v
	}
	ops, err := gqlParse(query)
	if err != nil {
		return map[string]any{"errors": []map[string]string{{"message": "parse error: " + err.Error()}}}
	}
	var op *gqlOp
	if opName != "" {
		for _, o := range ops {
			if o.name == opName {
				op = o
				break
			}
		}
		if op == nil {
			return map[string]any{"errors": []map[string]string{{"message": "unknown operation " + opName}}}
		}
	} else if len(ops) == 1 {
		op = ops[0]
	} else if len(ops) == 0 {
		return map[string]any{"errors": []map[string]string{{"message": "no operation"}}}
	} else {
		return map[string]any{"errors": []map[string]string{{"message": "multiple operations require operationName"}}}
	}
	// variable defaults
	for name, def := range op.varDefs {
		if _, ok := vars[name]; !ok {
			vars[name] = resolveGQLValue(def, vars)
		}
	}
	ex := &gqlExec{s: s, vars: vars}
	return ex.execute(op)
}
