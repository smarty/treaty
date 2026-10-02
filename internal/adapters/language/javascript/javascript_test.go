package javascript

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
)

const tricky = `#!/usr/bin/env node
import def, { a as b, type T } from "./a";
import * as ns from "./b";
import "./side-effect";
import fs = require("fs");

/** Docs with function fake() inside. */
export async function load<K extends string>(key: K, ...rest: Array<Map<string, number>>): Promise<void> {
  const ratio = rest.length / 2 / 1;
  const re = /}{/g;
  const text = ` + "`a ${ `b ${ \"}\" }` } c`" + `;
  await import("./lazy");
}

export function over(a: string): void;
export function over(a: number): void;
export function over(a: any) {}

export abstract class Base<T> extends Parent<T> implements One, Two {
  @observable
  protected value?: T;
  static readonly count = 0;
  abstract run(): void;
  async *walk(): AsyncGenerator<T> {}
  get = 1;
  private secret() {}
}

export type Shape =
  | { kind: "circle" }
  | { kind: "square" }

declare module "untyped" {
  export const x: number;
}

let x = 1, { y, z: [w] } = obj

export default {
  name: "thing",
}
`

func TestParseTricky(t *testing.T) {
	file := parseFile([]byte(tricky), false)
	var paths []string
	for _, spec := range file.imports {
		paths = append(paths, spec.path)
	}

	if want := []string{"./a", "./b", "./side-effect", "fs", "./lazy"}; !slices.Equal(paths, want) {
		t.Errorf("imports: got %q, want %q", paths, want)
	}

	if names := file.imports[0].names; len(names) != 3 || names[0] != (binding{"def", "default"}) || names[1] != (binding{"b", "a"}) || names[2] != (binding{"T", "T"}) {
		t.Errorf("named imports: %+v", names)
	}

	if names := file.imports[1].names; len(names) != 1 || names[0] != (binding{"ns", "*"}) {
		t.Errorf("namespace import: %+v", names)
	}

	byName := map[string][]*declaration{}
	for _, declaration := range file.declarations {
		byName[declaration.name] = append(byName[declaration.name], declaration)
	}

	for name, want := range map[string]string{
		"load":        "export async function load<K extends string>(key: K, ...rest: Array<Map<string, number>>): Promise<void>",
		"over":        "export function over(a: string): void",
		"Base":        "export abstract class Base<T> extends Parent<T> implements One, Two",
		"Base.run":    "abstract run(): void",
		"Base.walk":   "async *walk(): AsyncGenerator<T>",
		"Base.secret": "private secret()",
		"Shape":       `export type Shape = | { kind: "circle" } | { kind: "square" }`,
		"x":           "let x",
		"y":           "let y",
		"w":           "let w",
		"default":     `export default { name: "thing", }`,
	} {
		if len(byName[name]) == 0 {
			t.Errorf("%s: missing", name)
			continue
		}

		if got := byName[name][0].signature; got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}

	if len(byName["over"]) != 3 {
		t.Errorf("overloads: got %d declarations, want 3", len(byName["over"]))
	}

	if byName["x"][0].exported || !byName["load"][0].exported || !byName["default"][0].exported || file.exports["default"] != "default" {
		t.Errorf("export flags wrong: %+v", file.exports)
	}

	if !byName["Base.secret"][0].private || byName["Base.run"][0].private {
		t.Error("private members misread")
	}

	base := byName["Base"][0]
	var fieldTexts []string
	for _, field := range base.fields {
		fieldTexts = append(fieldTexts, field.Text)
	}

	if want := []string{"protected value?: T", "static readonly count", "get"}; !slices.Equal(fieldTexts, want) {
		t.Errorf("fields: got %q, want %q", fieldTexts, want)
	}

	if len(base.extends) != 1 || base.extends[0][0].text != "Parent" || len(base.implements) != 2 || base.implements[1][0].text != "Two" {
		t.Errorf("heritage: %v %v", base.extends, base.implements)
	}

	if _, ok := byName["untyped"]; ok {
		t.Error("ambient module declarations are not symbols")
	}
}

func TestLexer(t *testing.T) {
	for _, test := range []struct {
		name string
		src  string
		jsx  bool
		want []string
	}{
		{"division", "a / b / c", false, []string{"a", "/", "b", "/", "c"}},
		{"regex", "x = /a\\/[/]b/gi.test(s)", false, []string{"x", "=", "/a\\/[/]b/gi", ".", "test", "(", "s", ")"}},
		{"template", "`a ${b + `c ${d}`} e`", false, []string{"`a ${", "b", "+", "`c ${", "d", "}`", "} e`"}},
		{"jsx text", "return <p a=\"x\" b={c}>Don't {d}</p>", true, []string{"return", "p", "c", "d"}},
		{"jsx member and fragment", "f(<><X.Y z /></>)", true, []string{"f", "(", "X", ".", "Y", ")"}},
		{"less than", "if (a < b) {}", true, []string{"if", "(", "a", "<", "b", ")", "{", "}"}},
		{"tsx generic arrow", "const f = <T,>(x: T) => x", true, []string{"const", "f", "=", "<", "T", ",", ">", "(", "x", ":", "T", ")", "=>", "x"}},
		{"private name", "this.#x", false, []string{"this", ".", "#x"}},
		{"type arguments", "Map<string, Array<number>>", false, []string{"Map", "<", "string", ",", "Array", "<", "number", ">", ">"}},
	} {
		var got []string
		for _, current := range lex([]byte(test.src), test.jsx) {
			got = append(got, current.text)
		}

		if !slices.Equal(got, test.want) {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestFixtureGraph(t *testing.T) {
	g, err := NewExtractor().Extract("testdata/shop")
	if err != nil {
		t.Fatal(err)
	}

	var modules []string
	for _, module := range g.Modules {
		modules = append(modules, module.ID)
	}

	if want := []string{"js:lib", "js:src/ui", "js:src/util", "ts:packages/format/src", "ts:src/app", "ts:src/domain"}; !slices.Equal(modules, want) {
		t.Errorf("modules: got %q, want %q", modules, want)
	}

	for _, missing := range []string{"js:src/util:ignored", "js:dist:bundled", "ts:src/domain:Order.price"} {
		if g.Symbol(missing) != nil {
			t.Errorf("%s should not be extracted", missing)
		}
	}

	for id, want := range map[string]struct {
		kind     string
		contract bool
	}{
		"ts:src/domain:Order":             {graph.KindInterface, true},
		"ts:src/domain:Order.total":       {graph.KindMethod, true},
		"ts:src/domain:Status":            {graph.KindType, true},
		"ts:src/domain:placeOrder":        {graph.KindFunction, true},
		"ts:src/domain:discount":          {graph.KindFunction, true},
		"ts:src/domain:LIMIT":             {graph.KindValue, true},
		"ts:src/domain:audit":             {graph.KindFunction, true},
		"ts:src/domain:Money":             {graph.KindType, true},
		"ts:src/domain:Money.times":       {graph.KindMethod, true},
		"ts:src/domain:Money.format":      {graph.KindMethod, true},
		"ts:src/app:Checkout.constructor": {graph.KindMethod, true},
		"js:src/ui:Button":                {graph.KindFunction, true},
		"js:src/ui:pattern":               {graph.KindValue, false},
		"js:src/util:a/helper":            {graph.KindFunction, false},
		"js:src/util:b/helper":            {graph.KindFunction, false},
		"js:lib:sum":                      {graph.KindFunction, true},
		"js:lib:fmt":                      {graph.KindFunction, true},
		"js:lib:total":                    {graph.KindFunction, true},
		"js:lib:version":                  {graph.KindValue, true},
	} {
		symbol := g.Symbol(id)
		if symbol == nil {
			t.Errorf("%s: missing", id)
			continue
		}

		if symbol.Kind != want.kind || symbol.Contract != want.contract {
			t.Errorf("%s: got %s contract=%t, want %s contract=%t", id, symbol.Kind, symbol.Contract, want.kind, want.contract)
		}
	}

	if doc := g.Symbol("ts:src/domain:Order").Doc; doc != "An order a customer places." {
		t.Errorf("JSDoc: got %q", doc)
	}

	if money := g.Symbol("ts:src/domain:Money"); len(money.Fields) != 3 || money.Fields[0].Contract || money.Fields[1].Contract || !money.Fields[2].Contract {
		t.Errorf("Money fields: %+v", money.Fields)
	}

	edges := map[string]bool{}
	for _, edge := range g.Edges {
		edges[edge.From+" -"+edge.Kind+"-> "+edge.To] = true
	}

	for _, want := range []string{
		"ts:src/app:Checkout -implements-> ts:src/domain:Priced",
		"ts:src/domain:Order -embeds-> ts:src/domain:Priced",
		"ts:src/app:Checkout.submit -call-> ts:src/domain:placeOrder",
		"ts:src/app:Checkout.submit -call-> ts:src/app:Checkout.price",
		"ts:src/app:Checkout.submit -call-> ts:packages/format/src:format",
		"ts:src/app:Checkout.price -type-use-> ts:src/domain:Money",
		"ts:src/app:checkoutAll -type-use-> ts:src/domain:LIMIT",
		"js:src/ui:App -call-> js:src/ui:Button",
		"js:src/ui:App -type-use-> ts:src/app:Checkout",
		"js:src/ui:App -call-> ts:src/app:Checkout.submit",
		"js:src/util:one -call-> js:src/util:a/helper",
		"js:lib:sum -call-> js:src/util:one",
		"js:lib:sum -call-> js:src/util:two",
		"js:lib:total -call-> js:lib:sum",
		"ts:src/domain:discount -call-> ts:src/domain:Money.times",
	} {
		if !edges[want] {
			t.Errorf("missing edge %s", want)
		}
	}

	if edges["ts:src/app:Checkout -type-use-> ts:src/domain:Priced"] {
		t.Error("an implemented interface is an implements edge, not also a type use")
	}

	imports := map[string]bool{}
	for _, item := range g.Imports {
		imports[item.From+" -> "+item.To] = true
	}

	for _, want := range []string{"ts:src/app -> ts:src/domain", "ts:src/app -> ts:packages/format/src", "js:src/ui -> ts:src/app", "js:lib -> js:src/util"} {
		if !imports[want] {
			t.Errorf("missing import %s", want)
		}
	}

	if root := g.Module("ts:packages/format/src"); root.Manifest != "" {
		t.Errorf("a package.json one directory up is not this module's manifest: %q", root.Manifest)
	}
}

func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	copyTree(t, "testdata/shop", root)
	if err := os.Remove(filepath.Join(root, "src", "util", "b.js")); err != nil {
		t.Fatal(err)
	}

	service := newService(t, root)
	dumped, err := service.Dump("")
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := service.ParseGraph(dumped)
	if err != nil {
		t.Fatal(err)
	}

	extracted, _ := service.Extract()
	if differences := graph.Compare(extracted, parsed); len(differences) > 0 {
		t.Fatalf("round trip lost %d thing(s):\n%s", len(differences), strings.Join(differences[:min(len(differences), 20)], "\n"))
	}
}

func TestFixtureCheck(t *testing.T) {
	report, err := newService(t, "testdata/shop").Check("")
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, violation := range report.Violations {
		violations = append(violations, violation.From+" -> "+violation.To)
	}

	// The UI may drive the application, but the application may not reach
	// a driven adapter, and adapters may not reach each other.
	if want := []string{"js:lib -> js:src/util", "ts:src/app -> ts:packages/format/src"}; !slices.Equal(violations, want) {
		t.Errorf("violations: got %q, want %q", violations, want)
	}
}

func TestDialect(t *testing.T) {
	dialect := NewDialect(LanguageTypeScript)
	for text, want := range map[string]app.Declaration{
		"export function resolve(name: string): Thing":   {Kind: graph.KindFunction, Name: "resolve", Contract: true, Key: "resolve(string):Thing"},
		"function helper(a, b = 1)":                      {Kind: graph.KindFunction, Name: "helper", Key: "helper(_,_?)"},
		"export const handler = async (event: Event) =>": {Kind: graph.KindFunction, Name: "handler", Contract: true, Key: "async handler(Event)"},
		"exports.sum = function(a, b)":                   {Kind: graph.KindFunction, Name: "sum", Contract: true, Key: "sum(_,_)"},
		"export default class Store<T> extends Base":     {Kind: graph.KindType, Name: "Store", Contract: true, Key: "export default class Store<T> extends Base"},
		"export interface Port":                          {Kind: graph.KindInterface, Name: "Port", Contract: true, Key: "export interface Port"},
		"export type Id = string | number":               {Kind: graph.KindType, Name: "Id", Contract: true, Key: "export type Id = string | number"},
		"export const LIMIT: number = 100":               {Kind: graph.KindValue, Name: "LIMIT", Contract: true, Key: "export const LIMIT: number"},
		"export enum Color":                              {Kind: graph.KindType, Name: "Color", Contract: true, Key: "export enum Color"},
	} {
		got, err := dialect.Declare(text, nil)
		if err != nil || got != want {
			t.Errorf("%q: got %+v (%v), want %+v", text, got, err, want)
		}
	}

	class := &app.Declaration{Kind: graph.KindType, Name: "Store", Contract: true}
	for text, want := range map[string]app.Declaration{
		"save(item: T): Promise<void>": {Kind: graph.KindMethod, Name: "save", Contract: true, Key: "save(T):Promise<void>"},
		"private flush()":              {Kind: graph.KindMethod, Name: "flush", Key: "flush()"},
		"static readonly size: number": {Kind: app.KindField, Name: "size", Contract: true, Key: "static readonly size: number"},
		"#count":                       {Kind: app.KindField, Name: "#count", Key: "#count"},
		"onClick = (e: Event) =>":      {Kind: graph.KindMethod, Name: "onClick", Contract: true, Key: "onClick(Event)"},
	} {
		got, err := dialect.Declare(text, class)
		if err != nil || got != want {
			t.Errorf("%q in a class: got %+v (%v), want %+v", text, got, err, want)
		}
	}

	if got, err := dialect.Declare("total(currency: string): Money", &app.Declaration{Kind: graph.KindInterface, Name: "Order", Contract: true}); err != nil || got.Kind != graph.KindMethod {
		t.Errorf("interface method: got %+v (%v)", got, err)
	}

	for _, bad := range []string{"return x", "x + 1", ""} {
		if _, err := dialect.Declare(bad, nil); !errors.Is(err, ErrNotDeclaration) {
			t.Errorf("%q: want ErrNotDeclaration, got %v", bad, err)
		}
	}

	if dialect.ModulePath("src/app/checkout.ts") != "src/app" || dialect.ModulePath("src/app") != "src/app" || !dialect.IsFile("a.jsx") || dialect.IsFile("a.go") {
		t.Error("paths misread")
	}
}

func TestCompatible(t *testing.T) {
	dialect := NewDialect(LanguageJavaScript)
	for _, test := range []struct {
		kind, before, after string
		want                bool
	}{
		{graph.KindFunction, "export function f(a: string): void", "export function f(renamed: string): void", true},
		{graph.KindFunction, "export function f(a: string): void", "export function f(a: number): void", false},
		{graph.KindFunction, "export function f(a)", "export function f(a, b)", false},
		{graph.KindMethod, "save(item: T): void", "save(thing: T): void", true},
		{graph.KindValue, "export const LIMIT = 1", "export const LIMIT = 2", true},
		{graph.KindValue, "export const LIMIT: number = 1", "export const LIMIT: string = 1", false},
		{graph.KindValue, "export default {a: 1}", "export default {a: 2}", true},
	} {
		before := &graph.Symbol{Kind: test.kind, Signature: test.before}
		after := &graph.Symbol{Kind: test.kind, Signature: test.after}
		if got := dialect.Compatible(before, after); got != test.want {
			t.Errorf("%q -> %q: got %t, want %t", test.before, test.after, got, test.want)
		}
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, _ := filepath.Rel(from, current)
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newService(t *testing.T, root string) *app.Service {
	t.Helper()
	return app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect(LanguageJavaScript), NewDialect(LanguageTypeScript)},
		nil, filesystem.NewWorkspace(t.TempDir()), nil, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
}
