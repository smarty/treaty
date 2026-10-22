package csharp

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/smarty/treaty/internal/adapters/filesystem"
	"github.com/smarty/treaty/internal/adapters/gitvcs"
	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

// lexCase is one lexer case: the source and the expected tokens, each as
// kind:text.
type lexCase struct {
	name string
	src  string
	want []string
}

var lexCases = []lexCase{
	{"words", "public class Order", []string{"ident:public", "ident:class", "ident:Order"}},
	{"line comment", "a // note\nb", []string{"ident:a", "comment:// note", "ident:b"}},
	{"block comment", "a /* x\ny */ b", []string{"ident:a", "comment:/* x\ny */", "ident:b"}},
	{"doc comment", "/// <summary>Hi</summary>\nclass A", []string{"doc:/// <summary>Hi</summary>", "ident:class", "ident:A"}},
	{"regular string", `x = "a \"b\" c";`, []string{"ident:x", "op:=", `string:"a \"b\" c"`, "op:;"}},
	{"verbatim string", "x = @\"a \"\"b\"\"\nc\";", []string{"ident:x", "op:=", "string:@\"a \"\"b\"\"\nc\"", "op:;"}},
	{"interpolated string", `x = $"a {b("}")} {{c}}";`, []string{"ident:x", "op:=", `string:$"a {b("}")} {{c}}"`, "op:;"}},
	{"verbatim interpolated string", "x = $@\"a {b}\n\"\"c\"\"\" + @$\"d\";", []string{"ident:x", "op:=", "string:$@\"a {b}\n\"\"c\"\"\"", "op:+", "string:@$\"d\"", "op:;"}},
	{"raw string", `x = """a "quoted" b""";`, []string{"ident:x", "op:=", `string:"""a "quoted" b"""`, "op:;"}},
	{"interpolated raw string", `x = $$"""{ {{y}} }""";`, []string{"ident:x", "op:=", `string:$$"""{ {{y}} }"""`, "op:;"}},
	{"multi-line raw string", join(`x = """`, `  a`, `  """;`), []string{"ident:x", "op:=", "string:\"\"\"\n  a\n  \"\"\"", "op:;"}},
	{"u8 literal", `x = "abc"u8;`, []string{"ident:x", "op:=", `string:"abc"u8`, "op:;"}},
	{"char literals", `a = 'x'; b = '\''; c = 'A';`, []string{"ident:a", "op:=", "char:'x'", "op:;", "ident:b", "op:=", `char:'\''`, "op:;", "ident:c", "op:=", `char:'A'`, "op:;"}},
	{"verbatim identifier", "int @class;", []string{"ident:int", "ident:@class", "op:;"}},
	{"global qualifier", "global::System.String s;", []string{"ident:global", "op:::", "ident:System", "op:.", "ident:String", "ident:s", "op:;"}},
	{"byte order mark", "\ufeffusing System;", []string{"ident:using", "ident:System", "op:;"}},
	{"brace in string", `class A { string s = "{"; }`, []string{"ident:class", "ident:A", "op:{", "ident:string", "ident:s", "op:=", `string:"{"`, "op:;", "op:}"}},
	{"spaces in string", `x = "a  b";`, []string{"ident:x", "op:=", `string:"a  b"`, "op:;"}},
	{"preprocessor lines", join("#if A", "x;", "#elif B", "y;", "#else", "z;", "#endif"), []string{
		"directive:#if A", "ident:x", "op:;", "directive:#elif B", "ident:y", "op:;", "directive:#else", "ident:z", "op:;", "directive:#endif",
	}},
	{"whole methods in both branches", join("#if A", "void M() {}", "#else", "void N() {}", "#endif"), []string{
		"directive:#if A", "ident:void", "ident:M", "op:(", "op:)", "op:{", "op:}",
		"directive:#else", "ident:void", "ident:N", "op:(", "op:)", "op:{", "op:}", "directive:#endif",
	}},
	{"each branch opens a brace", join("#if A", "class B {", "#else", "class C {", "#endif", "}"), []string{
		"directive:#if A", "ident:class", "ident:B", "op:{", "directive:#endif", "op:}",
	}},
	{"split method header", join("#if A", "void M(int a)", "#else", "void M(string a)", "#endif", "{ }"), []string{
		"directive:#if A", "ident:void", "ident:M", "op:(", "ident:int", "ident:a", "op:)", "directive:#endif", "op:{", "op:}",
	}},
	{"split class header", join("#if A", "class C : IA", "#else", "class C", "#endif", "{ }"), []string{
		"directive:#if A", "ident:class", "ident:C", "op::", "ident:IA", "directive:#endif", "op:{", "op:}",
	}},
	{"inside a parameter list", join("void M(", "#if A", "int a;", "#else", "string a;", "#endif", ") {}"), []string{
		"ident:void", "ident:M", "op:(", "directive:#if A", "ident:int", "ident:a", "op:;", "directive:#endif", "op:)", "op:{", "op:}",
	}},
}

func TestLexer(t *testing.T) {
	for _, each := range lexCases {
		t.Run(each.name, func(t *testing.T) {
			tokens, err := lex([]byte(each.src))
			if err != nil {
				t.Fatalf("lex: %v", err)
			}

			var got []string
			for _, token := range tokens {
				got = append(got, token.kind.String()+":"+token.text)
			}

			if !slices.Equal(got, each.want) {
				t.Errorf("got  %q\nwant %q", got, each.want)
			}
		})
	}
}

func TestLexerFailures(t *testing.T) {
	for name, src := range map[string]string{
		"unterminated string":        `x = "abc`,
		"string across a line":       "x = \"abc\n\";",
		"unterminated verbatim":      `x = @"abc`,
		"unterminated raw string":    `x = """abc`,
		"unterminated interpolation": `x = $"a {b`,
		"unterminated char":          `x = 'a`,
		"unterminated block comment": "x /* abc",
		"unbalanced brackets":        "class A { void M() {}",
		"extra closing bracket":      "class A { } }",
		"if without endif":           join("#if A", "class B {}"),
		"endif without if":           join("class B {}", "#endif"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lex([]byte(src)); err == nil {
				t.Errorf("lex %q: got no error", src)
			}
		})
	}
}

// parseCase is one parser case: the source and the expected declarations,
// each as kind|name|parent|signature.
type parseCase struct {
	name string
	src  string
	want []string
}

var parseCases = []parseCase{
	{"table rows", join(
		"namespace Shop;",
		"public class Order",
		"{",
		"    public class Line { }",
		"    public void Add(Line line, int count) { }",
		"    public void Process(Outer.Inner x) { }",
		"    public void Log(String s) { }",
		"    public Order(int id) { }",
		"    static Order() { }",
		"    ~Order() { }",
		"    public Line this[int i] => null;",
		"    public T Map<T>(Func<T> f) => default;",
		"    public void Save(ref int x, out int y, in int z, params int[] rest) { y = 0; }",
		"    public int Count { get; set; }",
		"    public event EventHandler Changed;",
		"    public const int Limit = 10;",
		"    public static readonly Money Zero = new Money();",
		"    public int count;",
		"    int @class;",
		"}",
		"public interface IStore { void Save(Order o); int Count { get; } }",
		"public class Store : IStore { void IStore.Save(Order o) { } int IStore.Count { get { return 0; } } }",
		"public class Result<T> { public T Get(int i) => default; }",
		"public struct Money { public static Money operator +(Money a, Money b) => a; public static implicit operator Money(int x) => default; }",
		"public enum Color { Red = 1 }",
	), []string{
		"type|Order||public class Order",
		"type|Order.Line|Order|public class Line",
		"method|Order.Add(Line,int)|Order|public void Add(Line line, int count)",
		"method|Order.Process(Inner)|Order|public void Process(Outer.Inner x)",
		"method|Order.Log(string)|Order|public void Log(String s)",
		"method|Order.Order(int)|Order|public Order(int id)",
		"value|Order.this[int]|Order|public Line this[int i] { get; }",
		"method|Order.Map<>(Func<M0>)|Order|public T Map<T>(Func<T> f)",
		"method|Order.Save(int&,int&,int&,int[])|Order|public void Save(ref int x, out int y, in int z, params int[] rest)",
		"value|Order.Count|Order|public int Count { get; set; }",
		"value|Order.Changed|Order|public event EventHandler Changed",
		"value|Order.Limit|Order|public const int Limit = 10",
		"value|Order.Zero|Order|public static readonly Money Zero = new Money()",
		"value|Order.count|Order|public int count",
		"value|Order.class|Order|private int @class",
		"interface|IStore||public interface IStore",
		"method|IStore.Save(Order)|IStore|public void Save(Order o)",
		"value|IStore.Count|IStore|public int Count { get; }",
		"type|Store||public class Store : IStore",
		"type|Result<>||public class Result<T>",
		"method|Result<>.Get(int)|Result<>|public T Get(int i)",
		"type|Money||public struct Money",
		"method|Money.op_Addition(Money,Money)|Money|public static Money operator +(Money a, Money b)",
		"method|Money.op_Implicit(int)Money|Money|public static implicit operator Money(int x)",
		"type|Color||public enum Color",
		"value|Color.Red|Color|public Red = 1",
	}},
	{"qualified parameter types", join(
		"class A {",
		"    public void M(List<Outer.Inner> a) { }",
		"    public void N(global::A.B.C a, Alias::C b) { }",
		"    public void P((int id, Outer.Inner x) a) { }",
		"    public static implicit operator Outer.Inner(int x) => default;",
		"}",
	), []string{
		"type|A||internal class A",
		"method|A.M(List<Inner>)|A|public void M(List<Outer.Inner> a)",
		"method|A.N(C,C)|A|public void N(global::A.B.C a, Alias::C b)",
		"method|A.P((int,Inner))|A|public void P((int id, Outer.Inner x) a)",
		"method|A.op_Implicit(int)Inner|A|public static implicit operator Outer.Inner(int x)",
	}},
	{"keyword names", join(
		"class A {",
		"    void M(String a, System.String b, List<Int32> c) { }",
		"}",
	), []string{
		"type|A||internal class A",
		"method|A.M(string,string,List<int>)|A|private void M(String a, System.String b, List<Int32> c)",
	}},
	{"operator names", join(
		"struct M {",
		"    public static M operator checked +(M a, M b) => a;",
		"    public void operator +=(M b) { }",
		"    public static bool operator ==(M a, M b) => true;",
		"    public static M operator -(M a) => a;",
		"}",
	), []string{
		"type|M||internal struct M",
		"method|M.op_CheckedAddition(M,M)|M|public static M operator checked +(M a, M b)",
		"method|M.op_AdditionAssignment(M)|M|public void operator +=(M b)",
		"method|M.op_Equality(M,M)|M|public static bool operator ==(M a, M b)",
		"method|M.op_UnaryNegation(M)|M|public static M operator -(M a)",
	}},
	{"generic method in a generic type", join(
		"public class Result<TValue> {",
		"    public Result<TOut> Map<TOut>(Func<TValue, TOut> f) => default;",
		"}",
	), []string{
		"type|Result<>||public class Result<TValue>",
		"method|Result<>.Map<>(Func<T0,M0>)|Result<>|public Result<TOut> Map<TOut>(Func<TValue, TOut> f)",
	}},
	{"nested generic types", join(
		"public class Outer<T> {",
		"    public class Inner<U> {",
		"        public void M(T a, U b) { }",
		"    }",
		"}",
	), []string{
		"type|Outer<>||public class Outer<T>",
		"type|Outer<>.Inner<>|Outer<>|public class Inner<U>",
		"method|Outer<>.Inner<>.M(T0,T1)|Outer<>.Inner<>|public void M(T a, U b)",
	}},
	{"renamed type parameter", join(
		"public class Result<TRenamed> {",
		"    public Result<TOut> Map<TOut>(Func<TRenamed, TOut> f) => default;",
		"}",
	), []string{
		"type|Result<>||public class Result<TRenamed>",
		"method|Result<>.Map<>(Func<T0,M0>)|Result<>|public Result<TOut> Map<TOut>(Func<TRenamed, TOut> f)",
	}},
	{"hidden members", join(
		"public class Store : IStore {",
		"    static Store() { Init(); }",
		"    ~Store() { }",
		"    void IStore.Save(Order o) { }",
		"    int IStore.Count => 0;",
		"    event EventHandler IStore.Changed { add { } remove { } }",
		"    public void Keep() { }",
		"}",
	), []string{
		"type|Store||public class Store : IStore",
		"method|Store.Keep()|Store|public void Keep()",
	}},
	{"enum with no values", "enum E { A, B }", []string{
		"type|E||internal enum E",
		"value|E.A|E|public A = 0",
		"value|E.B|E|public B = 1",
	}},
	{"enum after a value", "public enum Color { Red = 1, Green }", []string{
		"type|Color||public enum Color",
		"value|Color.Red|Color|public Red = 1",
		"value|Color.Green|Color|public Green = 2",
	}},
	{"enum after a non-literal value", "enum F { A = 1, C = 2, B = A | C, D }", []string{
		"type|F||internal enum F",
		"value|F.A|F|public A = 1",
		"value|F.C|F|public C = 2",
		"value|F.B|F|public B = A | C",
		"value|F.D|F|public D",
	}},
	{"record primary constructor", "public record Point(int X, int Y);", []string{
		"type|Point||public record Point",
		"method|Point.Point(int,int)|Point|public Point(int X, int Y)",
		"value|Point.X|Point|public int X { get; init; }",
		"value|Point.Y|Point|public int Y { get; init; }",
	}},
	{"class primary constructor", "public class Service(ILogger log) : Base(log) { }", []string{
		"type|Service||public class Service : Base",
		"method|Service.Service(ILogger)|Service|public Service(ILogger log)",
	}},
	{"record class", "public record class Point { }", []string{
		"type|Point||public record Point",
	}},
	{"attributes", join(
		"[Serializable]",
		"public class A {",
		"    [Obsolete(\"no\")] public void M([NotNull] string s) { }",
		"}",
	), []string{
		"type|A||public class A",
		"method|A.M(string)|A|public void M(string s)",
	}},
	{"multi-line signature", join(
		"class A {",
		"    public void M(",
		"        int a,",
		"        int b) { }",
		"}",
	), []string{
		"type|A||internal class A",
		"method|A.M(int,int)|A|public void M(int a, int b)",
	}},
	{"comments in a parameter list", join(
		"class A {",
		"    public void M(int a, // first",
		"        int b /* second */) { }",
		"}",
	), []string{
		"type|A||internal class A",
		"method|A.M(int,int)|A|public void M(int a, int b)",
	}},
	{"spaces in a const string", `class A { public const string S = "a  b"; }`, []string{
		"type|A||internal class A",
		`value|A.S|A|public const string S = "a  b"`,
	}},
	{"multi-line raw const", join(
		"class A {",
		`    public const string S = """`,
		"        line one",
		"\tline two",
		`        """;`,
		"}",
	), []string{
		"type|A||internal class A",
		`value|A.S|A|public const string S = """\n        line one\n\tline two\n        """`,
	}},
	{"verbatim identifiers", "class A { int @ptr; int @class; }", []string{
		"type|A||internal class A",
		"value|A.ptr|A|private int ptr",
		"value|A.class|A|private int @class",
	}},
	{"property signatures", join(
		"public class A {",
		"    public int B { get; private set; }",
		"    public int C { get { return c; } set { c = value; } }",
		"    public int D => n;",
		"    public int this[int i] { get => 0; init { } }",
		"    public event EventHandler E { add { } remove { } }",
		"    public required string F { get; init; } = \"\";",
		"}",
	), []string{
		"type|A||public class A",
		"value|A.B|A|public int B { get; private set; }",
		"value|A.C|A|public int C { get; set; }",
		"value|A.D|A|public int D { get; }",
		"value|A.this[int]|A|public int this[int i] { get; init; }",
		"value|A.E|A|public event EventHandler E",
		"value|A.F|A|public required string F { get; init; }",
	}},
	{"no modifier", "class A { void M() { } }", []string{
		"type|A||internal class A",
		"method|A.M()|A|private void M()",
	}},
}

func TestParseNames(t *testing.T) {
	for _, each := range parseCases {
		t.Run(each.name, func(t *testing.T) {
			file, err := parseFile([]byte(each.src))
			if err != nil {
				t.Fatalf("parseFile: %v", err)
			}

			var got []string
			for _, declaration := range file.declarations {
				got = append(got, describe(declaration))
			}

			if !slices.Equal(got, each.want) {
				t.Errorf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(each.want, "\n  "))
			}
		})
	}
}

func TestParseNamespaces(t *testing.T) {
	for name, each := range map[string]struct {
		src  string
		want map[string]string
	}{
		"file-scoped": {"namespace Shop.Domain;\nclass A { class B { } }", map[string]string{"A": "Shop.Domain", "A.B": "Shop.Domain"}},
		"nested blocks": {join(
			"namespace Shop {",
			"    class A { }",
			"    namespace Domain.Model {",
			"        class B { }",
			"    }",
			"}",
			"class C { }",
		), map[string]string{"A": "Shop", "B": "Shop.Domain.Model", "C": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			file, err := parseFile([]byte(each.src))
			if err != nil {
				t.Fatalf("parseFile: %v", err)
			}

			got := map[string]string{}
			for _, declaration := range file.declarations {
				got[declaration.name] = declaration.namespace
			}

			if !maps.Equal(got, each.want) {
				t.Errorf("got %v, want %v", got, each.want)
			}
		})
	}
}

func TestParseBodies(t *testing.T) {
	file, err := parseFile([]byte("class A { int M() => Helper.Run(1); }"))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}

	if got := texts(file.declarations[1].body); !slices.Equal(got, []string{"Helper", ".", "Run", "(", "1", ")"}) {
		t.Errorf("expression body: got %q", got)
	}
}

func TestParseProgram(t *testing.T) {
	file, err := parseFile([]byte(join(
		"using Shop.Domain;",
		"var order = new Order();",
		"Run(order);",
		"class Helper { }",
	)))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}

	program := file.declarations[0]
	if describe(program) != "function|<Program>||<Program>" || !file.program {
		t.Fatalf("got %q, program %v", describe(program), file.program)
	}

	if got := texts(program.body); !slices.Equal(got, []string{"var", "order", "=", "new", "Order", "(", ")", ";", "Run", "(", "order", ")", ";"}) {
		t.Errorf("program body: got %q", got)
	}

	main, err := parseFile([]byte("class P { static void Main(string[] args) { } }"))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}

	if !main.program {
		t.Errorf("static Main: file is not a program entry")
	}
}

func TestParseSplitHeaders(t *testing.T) {
	file, err := parseFile([]byte(join(
		"class A {",
		"#if NET",
		"    public void M(ReadOnlySpan<char> s)",
		"#else",
		"    public void M(string s)",
		"#endif",
		"    { }",
		"}",
		"#if NET",
		"public class B : IA",
		"#else",
		"public class B",
		"#endif",
		"{ }",
	)))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}

	var got []string
	for _, declaration := range file.declarations {
		got = append(got, describe(declaration))
	}

	want := []string{
		"type|A||internal class A",
		"method|A.M(ReadOnlySpan<char>)|A|public void M(ReadOnlySpan<char> s)",
		"type|B||public class B : IA",
	}

	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAccess(t *testing.T) {
	file, err := parseFile([]byte(join(
		"public class P {",
		"    public void A() { }",
		"    protected void B() { }",
		"    internal void C() { }",
		"    protected internal void D() { }",
		"    private protected void E() { }",
		"    private void F() { }",
		"    void G() { }",
		"}",
		"file class Q { public void H() { } }",
		"class R { public void I() { } }",
		"public interface S { void J(); }",
		"public enum T { K }",
		"internal class U { protected class V { public void L() { } } }",
		"public class W { protected class X { public void M() { } } private class Y { public void N() { } } }",
	)))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}

	got := map[string]string{}
	for _, declaration := range file.declarations {
		got[declaration.name] = fmt.Sprintf("%v %v", declaration.assembly, declaration.directory)
	}

	want := map[string]string{
		"P": "true true", "P.A()": "true true", "P.B()": "true true", "P.C()": "false true", "P.D()": "true true",
		"P.E()": "false true", "P.F()": "false false", "P.G()": "false false",
		"Q": "false false", "Q.H()": "false false",
		"R": "false true", "R.I()": "false true",
		"S": "true true", "S.J()": "true true",
		"T": "true true", "T.K": "true true",
		"U": "false true", "U.V": "false true", "U.V.L()": "false true",
		"W": "true true", "W.X": "true true", "W.X.M()": "true true", "W.Y": "false false", "W.Y.N()": "false false",
	}

	if !maps.Equal(got, want) {
		for name := range want {
			if got[name] != want[name] {
				t.Errorf("%s: got %q, want %q", name, got[name], want[name])
			}
		}
	}
}

const tricky = `using System;
using static System.Math;
using Map = System.Collections.Generic.Dictionary<string, int>;

namespace Shop.Tricky
{
    public class Box<T> where T : class
    {
        private Dictionary<string, List<List<int>>> nested = new();
        public Func<int, int> Twice = x => x * 2;

        public string Show(T item)
        {
            var text = $"{item} {(item is null ? "none" : $"{{nested}} {item}")}";
            Action act = () => { if (text.Length > 1) { Console.WriteLine(text); } };
            return text;
        }

#if DEBUG
        public void Trace() { }
#endif

        public IEnumerable<T> Items() { yield break; }
    }
}
`

func TestParseTricky(t *testing.T) {
	file, err := parseFile([]byte(tricky))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}

	var got []string
	for _, declaration := range file.declarations {
		got = append(got, describe(declaration))
	}

	want := []string{
		"type|Box<>||public class Box<T> where T : class",
		"value|Box<>.nested|Box<>|private Dictionary<string, List<List<int>>> nested = new()",
		"value|Box<>.Twice|Box<>|public Func<int, int> Twice = x => x * 2",
		"method|Box<>.Show(T0)|Box<>|public string Show(T item)",
		"method|Box<>.Trace()|Box<>|public void Trace()",
		"method|Box<>.Items()|Box<>|public IEnumerable<T> Items()",
	}

	if !slices.Equal(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	var usings []string
	for _, each := range file.usings {
		usings = append(usings, fmt.Sprintf("%s|%s|%v", each.alias, each.target, each.static))
	}

	if want := []string{"|System|false", "|System.Math|true", "Map|System.Collections.Generic.Dictionary<string, int>|false"}; !slices.Equal(usings, want) {
		t.Errorf("usings: got %q, want %q", usings, want)
	}
}

// sdkProject writes a .csproj with the default SDK around a body.
func sdkProject(body string) string {
	return `<Project Sdk="Microsoft.NET.Sdk">` + body + `</Project>`
}

// writeTree writes files under a new temporary directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// loadProjects writes a tree and reads its project files.
func loadProjects(t *testing.T, files map[string]string) *projectSet {
	t.Helper()
	root := writeTree(t, files)
	var paths []string
	for name := range files {
		if strings.HasSuffix(name, ".csproj") || path.Base(name) == "Directory.Build.props" {
			paths = append(paths, name)
		}
	}

	sort.Strings(paths)
	result, err := readProjects(root, paths)
	if err != nil {
		t.Fatalf("readProjects: %v", err)
	}

	return result
}

func TestProjects(t *testing.T) {
	signals := map[string]string{
		"Microsoft.NET.Test.Sdk": sdkProject(`<ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.0.0" /></ItemGroup>`),
		"xunit":                  sdkProject(`<ItemGroup><PackageReference Include="xunit" /></ItemGroup>`),
		"xunit.v3":               sdkProject(`<ItemGroup><PackageReference Include="xunit.v3" /></ItemGroup>`),
		"NUnit":                  sdkProject(`<ItemGroup><PackageReference Include="NUnit" /></ItemGroup>`),
		"MSTest":                 sdkProject(`<ItemGroup><PackageReference Include="MSTest" /></ItemGroup>`),
		"MSTest.TestFramework":   sdkProject(`<ItemGroup><PackageReference Include="MSTest.TestFramework" /></ItemGroup>`),
		"TUnit":                  sdkProject(`<ItemGroup><PackageReference Include="TUnit" /></ItemGroup>`),
		"legacy xunit.core":      `<Project xmlns="http://schemas.microsoft.com/developer/msbuild/2003"><ItemGroup><Reference Include="xunit.core, Version=2.4.0" /></ItemGroup></Project>`,
		"legacy MSTest":          `<Project><ItemGroup><Reference Include="Microsoft.VisualStudio.QualityTools.UnitTestFramework" /></ItemGroup></Project>`,
		"IsTestProject":          sdkProject(`<PropertyGroup><IsTestProject>true</IsTestProject></PropertyGroup>`),
		"MSTest.Sdk":             `<Project Sdk="MSTest.Sdk"></Project>`,
	}

	for name, content := range signals {
		t.Run("signal "+name, func(t *testing.T) {
			set := loadProjects(t, map[string]string{"P/P.csproj": content})
			if !set.byPath["P/P.csproj"].test {
				t.Errorf("not a test project")
			}
		})
	}

	for _, each := range projectCases {
		t.Run(each.name, func(t *testing.T) {
			each.check(t, loadProjects(t, each.files))
		})
	}
}

// projectCase is one project reader case: a tree and a check of what the
// reader found.
type projectCase struct {
	name  string
	files map[string]string
	check func(t *testing.T, set *projectSet)
}

// isTest checks the test flag of each project.
func isTest(want map[string]bool) func(t *testing.T, set *projectSet) {
	return func(t *testing.T, set *projectSet) {
		for name, test := range want {
			if project := set.byPath[name]; project == nil || project.test != test {
				t.Errorf("%s: test project should be %v", name, test)
			}
		}
	}
}

var projectCases = []projectCase{
	{"legacy nunit reference", map[string]string{
		"P/P.csproj": `<Project><ItemGroup><Reference Include="nunit.framework, Version=3.0.0.0, Culture=neutral" /></ItemGroup></Project>`,
	}, isTest(map[string]bool{"P/P.csproj": true})},
	{"test project by name", map[string]string{
		"Shop.UnitTests/Shop.UnitTests.csproj": sdkProject(""),
		"Shop.Domain/Shop.Domain.csproj":       sdkProject(""),
	}, isTest(map[string]bool{"Shop.UnitTests/Shop.UnitTests.csproj": true, "Shop.Domain/Shop.Domain.csproj": false})},
	{"sdk with a version", map[string]string{
		"P/P.csproj": `<Project Sdk="MSTest.Sdk/3.6.0"></Project>`,
	}, isTest(map[string]bool{"P/P.csproj": true})},
	{"props in a tests directory", map[string]string{
		"tests/Directory.Build.props": `<Project><ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" /></ItemGroup></Project>`,
		"tests/A/A.csproj":            sdkProject(""),
		"src/B/B.csproj":              sdkProject(""),
	}, isTest(map[string]bool{"tests/A/A.csproj": true, "src/B/B.csproj": false})},
	{"conditioned item group", map[string]string{
		"Directory.Build.props": `<Project><ItemGroup Condition="$(MSBuildProjectName.EndsWith('Specs'))"><PackageReference Include="xunit" /></ItemGroup></Project>`,
		"src/A/A.csproj":        sdkProject(""),
	}, isTest(map[string]bool{"src/A/A.csproj": false})},
	{"conditioned package reference", map[string]string{
		"src/A/A.csproj": sdkProject(`<ItemGroup><PackageReference Include="xunit" Condition="'$(Test)' == 'true'" /></ItemGroup>`),
	}, isTest(map[string]bool{"src/A/A.csproj": false})},
	{"nearer props without import", map[string]string{
		"Directory.Build.props":     `<Project><ItemGroup><PackageReference Include="xunit" /></ItemGroup></Project>`,
		"src/Directory.Build.props": `<Project><PropertyGroup><Nullable>enable</Nullable></PropertyGroup></Project>`,
		"src/A/A.csproj":            sdkProject(""),
	}, isTest(map[string]bool{"src/A/A.csproj": false})},
	{"nearer props imports its parent", map[string]string{
		"Directory.Build.props":     `<Project><ItemGroup><PackageReference Include="xunit" /></ItemGroup></Project>`,
		"src/Directory.Build.props": `<Project><Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(MSBuildThisFileDirectory)../'))" /></Project>`,
		"src/A/A.csproj":            sdkProject(""),
	}, isTest(map[string]bool{"src/A/A.csproj": true})},
	{"nearer props imports its parent by directory", map[string]string{
		"Directory.Build.props":     `<Project><ItemGroup><PackageReference Include="xunit" /></ItemGroup></Project>`,
		"src/Directory.Build.props": `<Project><Import Project="$([MSBuild]::GetDirectoryNameOfFileAbove($(MSBuildThisFileDirectory).., Directory.Build.props))\Directory.Build.props" /></Project>`,
		"src/A/A.csproj":            sdkProject(""),
	}, isTest(map[string]bool{"src/A/A.csproj": true})},
	{"executable signals", map[string]string{
		"Exe/Exe.csproj":       sdkProject(`<PropertyGroup><OutputType>Exe</OutputType></PropertyGroup>`),
		"Win/Win.csproj":       sdkProject(`<PropertyGroup><OutputType>WinExe</OutputType></PropertyGroup>`),
		"Web/Web.csproj":       `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"Worker/Worker.csproj": `<Project Sdk="Microsoft.NET.Sdk.Worker"></Project>`,
		"Blazor/Blazor.csproj": `<Project Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"></Project>`,
		"Lib/Lib.csproj":       sdkProject(""),
	}, func(t *testing.T, set *projectSet) {
		for name, want := range map[string]bool{"Exe/Exe.csproj": true, "Win/Win.csproj": true, "Web/Web.csproj": true, "Worker/Worker.csproj": true, "Blazor/Blazor.csproj": true, "Lib/Lib.csproj": false} {
			if got := set.byPath[name].executable; got != want {
				t.Errorf("%s: executable %v, want %v", name, got, want)
			}
		}
	}},
	{"web sdk library", map[string]string{
		"Web/Web.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web"><PropertyGroup><OutputType>Library</OutputType></PropertyGroup></Project>`,
	}, func(t *testing.T, set *projectSet) {
		if set.byPath["Web/Web.csproj"].executable {
			t.Errorf("a Library output is executable")
		}
	}},
	{"xunit v3 executable", map[string]string{
		"T/T.csproj": sdkProject(`<PropertyGroup><OutputType>Exe</OutputType></PropertyGroup><ItemGroup><PackageReference Include="xunit.v3" /></ItemGroup>`),
	}, isTest(map[string]bool{"T/T.csproj": true})},
	{"assembly name", map[string]string{
		"A/A.csproj": sdkProject(`<PropertyGroup><AssemblyName>Shop.Core</AssemblyName></PropertyGroup>`),
		"B/B.csproj": sdkProject(""),
	}, func(t *testing.T, set *projectSet) {
		if got := set.byPath["A/A.csproj"].name; got != "Shop.Core" {
			t.Errorf("A: name %q", got)
		}

		if got := set.byPath["B/B.csproj"].name; got != "B" {
			t.Errorf("B: name %q", got)
		}
	}},
	{"using items", map[string]string{
		"A/A.csproj": sdkProject(`<ItemGroup><Using Include="Shop.Domain" /><Using Include="Shop.Math" Static="true" /><Using Include="Shop.Domain.Order" Alias="Purchase" /></ItemGroup>`),
	}, func(t *testing.T, set *projectSet) {
		var got []string
		for _, each := range set.byPath["A/A.csproj"].usings {
			got = append(got, fmt.Sprintf("%s|%s|%v|%v", each.alias, each.target, each.static, each.global))
		}

		if want := []string{"|Shop.Domain|false|true", "|Shop.Math|true|true", "Purchase|Shop.Domain.Order|false|true"}; !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}},
	{"transitive references", map[string]string{
		"A/A.csproj": sdkProject(`<ItemGroup><ProjectReference Include="../B/B.csproj" /></ItemGroup>`),
		"B/B.csproj": sdkProject(`<ItemGroup><ProjectReference Include="../C/C.csproj" /></ItemGroup>`),
		"C/C.csproj": sdkProject(""),
	}, closureOf("A/A.csproj", "A/A.csproj", "B/B.csproj", "C/C.csproj")},
	{"backslash reference", map[string]string{
		"A/A.csproj": sdkProject(`<ItemGroup><ProjectReference Include="..\B\B.csproj" /></ItemGroup>`),
		"B/B.csproj": sdkProject(""),
	}, closureOf("A/A.csproj", "A/A.csproj", "B/B.csproj")},
	{"unreadable project files", map[string]string{
		"A/A.csproj": `<?xml version="1.0" encoding="utf-16"?><Project></Project>`,
		"B/B.csproj": `<Project><PropertyGroup></Project>`,
		"C/C.csproj": sdkProject(""),
	}, func(t *testing.T, set *projectSet) {
		var got []string
		for _, each := range set.skipped {
			got = append(got, each.path)
		}

		if !slices.Equal(got, []string{"A/A.csproj", "B/B.csproj"}) || set.byPath["A/A.csproj"] != nil || set.byPath["C/C.csproj"] == nil {
			t.Errorf("skipped %q", got)
		}
	}},
	{"library references an executable", map[string]string{
		"App/App.csproj": sdkProject(`<PropertyGroup><OutputType>Exe</OutputType></PropertyGroup>`),
		"Lib/Lib.csproj": sdkProject(`<ItemGroup><ProjectReference Include="../App/App.csproj" /></ItemGroup>`),
	}, func(t *testing.T, set *projectSet) {
		if set.entry(set.byPath["App/App.csproj"]) {
			t.Errorf("a referenced executable gives an entry")
		}
	}},
	{"test references an executable", map[string]string{
		"App/App.csproj":     sdkProject(`<PropertyGroup><OutputType>Exe</OutputType></PropertyGroup>`),
		"Tests/Tests.csproj": sdkProject(`<ItemGroup><PackageReference Include="xunit" /><ProjectReference Include="../App/App.csproj" /></ItemGroup>`),
	}, func(t *testing.T, set *projectSet) {
		if !set.entry(set.byPath["App/App.csproj"]) {
			t.Errorf("an executable that only a test project references gives no entry")
		}
	}},
	{"files with no project", map[string]string{
		"A/A.csproj": sdkProject(""),
	}, func(t *testing.T, set *projectSet) {
		loose := set.owner("Scripts/Player.cs")
		if loose == nil || loose != set.owner("Other/Enemy.cs") || len(loose.references) != 0 || loose.test || loose.executable || set.entry(loose) {
			t.Errorf("implicit project: %+v", loose)
		}

		if set.owner("A/Sub/Order.cs") != set.byPath["A/A.csproj"] {
			t.Errorf("A/Sub/Order.cs is not in A")
		}
	}},
}

// closureOf checks the projects a project can see.
func closureOf(name string, want ...string) func(t *testing.T, set *projectSet) {
	return func(t *testing.T, set *projectSet) {
		var got []string
		for project := range set.closure(set.byPath[name]) {
			got = append(got, project.path)
		}

		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("closure of %s: got %q, want %q", name, got, want)
		}
	}
}

// extractShop extracts a copy of the shop fixture.
func extractShop(t *testing.T) (*Extractor, *graph.Graph) {
	t.Helper()
	extractor := NewExtractor()
	g, err := extractor.Extract(shopCopy(t))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	return extractor, g
}

func TestFixtureGraph(t *testing.T) {
	extractor, g := extractShop(t)
	var modules []string
	for _, module := range g.Modules {
		modules = append(modules, fmt.Sprintf("%s name=%s entry=%v private=%v manifest=%s", module.ID, module.Name, module.Entry, module.Private, module.Manifest))
	}

	sort.Strings(modules)
	want := []string{
		"cs:src/Shop.Adapters name=Shop.Adapters entry=false private=false manifest=src/Shop.Adapters/Shop.Adapters.csproj",
		"cs:src/Shop.App name=Shop.App entry=false private=false manifest=src/Shop.App/Shop.App.csproj",
		"cs:src/Shop.Domain name=Shop.Domain entry=false private=false manifest=src/Shop.Domain/Shop.Domain.csproj",
		"cs:src/Shop.Domain/Catalog name= entry=false private=false manifest=",
		"cs:src/Shop.Domain/Catalog/Taxes name= entry=false private=true manifest=",
		"cs:src/Shop.Domain/Internal name= entry=false private=true manifest=",
		"cs:src/Shop.Domain/Rules name= entry=false private=false manifest=",
		"cs:src/Shop.Web name=Shop.Web entry=true private=true manifest=src/Shop.Web/Shop.Web.csproj",
	}

	if !slices.Equal(modules, want) {
		t.Errorf("modules:\n  %s\nwant:\n  %s", strings.Join(modules, "\n  "), strings.Join(want, "\n  "))
	}

	var files []string
	for _, module := range g.Modules {
		files = append(files, module.Files...)
	}

	for _, file := range files {
		for _, unwanted := range []string{"tests/", "/bin/", "/obj/", ".g.cs", ".Designer.cs", "Mapping.cs", "Broken.cs"} {
			if strings.Contains(file, unwanted) {
				t.Errorf("file %s should be skipped", file)
			}
		}
	}

	if skipped := extractor.Skipped(); len(skipped) != 1 || skipped[0].path != "src/Shop.Domain/Broken.cs" || skipped[0].reason == "" {
		t.Errorf("skipped: %+v", skipped)
	}

	if web := g.Module("cs:src/Shop.Web"); web == nil || !web.Entry {
		t.Errorf("Shop.Web is not an entry")
	}

	for module, file := range map[string]string{"cs:src/Shop.Domain/Catalog": "src/Shop.Domain/Catalog/Product.cs", "cs:src/Shop.Domain/Catalog/Taxes": "src/Shop.Domain/Catalog/Taxes/Product.Pricing.cs"} {
		if found := g.Module(module); found == nil || !slices.Contains(found.Files, file) {
			t.Errorf("%s does not list %s", module, file)
		}
	}
}

func TestFixtureSymbols(t *testing.T) {
	_, g := extractShop(t)
	var got []string
	for _, symbol := range g.Symbols {
		got = append(got, fmt.Sprintf("%s|%s|%s|%s|%v", symbol.ID, symbol.Kind, symbol.Parent, strings.TrimPrefix(symbol.File, "src/"), symbol.Contract))
	}

	sort.Strings(got)
	want := []string{
		"cs:src/Shop.Adapters:Retry|type||Shop.Adapters/Retry.cs|true",
		"cs:src/Shop.Adapters:Retry.Times(int)|method|Retry|Shop.Adapters/Retry.cs|true",
		"cs:src/Shop.Domain/Internal:Guard|type||Shop.Domain/Internal/Guard.cs|true",
		"cs:src/Shop.Domain/Internal:Guard.NotNegative(int)|method|Guard|Shop.Domain/Internal/Guard.cs|true",
		"cs:src/Shop.Adapters:CachedOrderStore|type||Shop.Adapters/CachedOrderStore.cs|true",
		"cs:src/Shop.Adapters:CachedOrderStore.Cut()|method|CachedOrderStore|Shop.Adapters/CachedOrderStore.cs|true",
		"cs:src/Shop.Adapters:SqlOrderStore|type||Shop.Adapters/SqlOrderStore.cs|true",
		"cs:src/Shop.Adapters:SqlOrderStore.Catalog()|method|SqlOrderStore|Shop.Adapters/SqlOrderStore.cs|true",
		"cs:src/Shop.Adapters:SqlOrderStore.Find(int)|method|SqlOrderStore|Shop.Adapters/SqlOrderStore.cs|true",
		"cs:src/Shop.Adapters:SqlOrderStore.Save(Order)|method|SqlOrderStore|Shop.Adapters/SqlOrderStore.cs|true",
		"cs:src/Shop.Adapters:SqlOrderStore.rows|value|SqlOrderStore|Shop.Adapters/SqlOrderStore.cs|false",
		"cs:src/Shop.App:PlaceOrder|type||Shop.App/PlaceOrder.cs|true",
		"cs:src/Shop.App:PlaceOrder.Audit()|method|PlaceOrder|Shop.App/PlaceOrder.cs|true",
		"cs:src/Shop.App:PlaceOrder.PlaceOrder(IOrderStore)|method|PlaceOrder|Shop.App/PlaceOrder.cs|true",
		"cs:src/Shop.App:PlaceOrder.Run(int)|method|PlaceOrder|Shop.App/PlaceOrder.cs|true",
		"cs:src/Shop.Domain/Catalog:Product|type||Shop.Domain/Catalog/Product.cs|true",
		"cs:src/Shop.Domain/Catalog:Product.CompareTo(Product?)|method|Product|Shop.Domain/Catalog/Taxes/Product.Pricing.cs|true",
		"cs:src/Shop.Domain/Catalog:Product.Name|value|Product|Shop.Domain/Catalog/Product.cs|true",
		"cs:src/Shop.Domain/Catalog:Product.Price()|method|Product|Shop.Domain/Catalog/Taxes/Product.Pricing.cs|true",
		"cs:src/Shop.Domain/Rules:Order/Order|type||Shop.Domain/Rules/Order.cs|true",
		"cs:src/Shop.Domain/Rules:Order/Order.Valid()|method|Order/Order|Shop.Domain/Rules/Order.cs|true",
		"cs:src/Shop.Domain/Rules:Order_Validation/Order|type||Shop.Domain/Rules/Order.Validation.cs|true",
		"cs:src/Shop.Domain/Rules:Order_Validation/Order.Valid()|method|Order_Validation/Order|Shop.Domain/Rules/Order.Validation.cs|true",
		"cs:src/Shop.Domain:IOrderStore|interface||Shop.Domain/IOrderStore.cs|true",
		"cs:src/Shop.Domain:IOrderStore.Count|value|IOrderStore|Shop.Domain/IOrderStore.cs|true",
		"cs:src/Shop.Domain:IOrderStore.Find(int)|method|IOrderStore|Shop.Domain/IOrderStore.cs|true",
		"cs:src/Shop.Domain:IOrderStore.Save(Order)|method|IOrderStore|Shop.Domain/IOrderStore.cs|true",
		"cs:src/Shop.Domain:Order|type||Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Add(Line)|method|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Add(Line,int)|method|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Banner|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Customer|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Describe()|method|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Empty|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Id|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Limit|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Line|type|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Line.Kind|type|Order.Line|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Line.Kind.Air|value|Order.Line.Kind|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Line.Kind.Ground|value|Order.Line.Kind|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Line.Price|value|Order.Line|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Line.Total(int)|method|Order.Line|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Order(int)|method|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Ship(Kind,int)|method|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.Status|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:Order.event|value|Order|Shop.Domain/Order.cs|false",
		"cs:src/Shop.Domain:Order.lines|value|Order|Shop.Domain/Order.cs|false",
		"cs:src/Shop.Domain:Order.this[int]|value|Order|Shop.Domain/Order.cs|true",
		"cs:src/Shop.Domain:OrderExtensions|type||Shop.Domain/OrderExtensions.cs|true",
		"cs:src/Shop.Domain:OrderExtensions.Total(Order,decimal)|method|OrderExtensions|Shop.Domain/OrderExtensions.cs|true",
		"cs:src/Shop.Domain:OrderStatus|type||Shop.Domain/OrderStatus.cs|true",
		"cs:src/Shop.Domain:OrderStatus.New|value|OrderStatus|Shop.Domain/OrderStatus.cs|true",
		"cs:src/Shop.Domain:OrderStatus.Paid|value|OrderStatus|Shop.Domain/OrderStatus.cs|true",
		"cs:src/Shop.Domain:OrderStatus.Shipped|value|OrderStatus|Shop.Domain/OrderStatus.cs|true",
		"cs:src/Shop.Domain:Pricing|type||Shop.Domain/Pricing.cs|true",
		"cs:src/Shop.Domain:Pricing.Discount(decimal)|method|Pricing|Shop.Domain/Pricing.cs|true",
		"cs:src/Shop.Domain:Result<>|type||Shop.Domain/Result.cs|true",
		"cs:src/Shop.Domain:Result<>.Map<>(Func<T0,M0>)|method|Result<>|Shop.Domain/Result.cs|true",
		"cs:src/Shop.Domain:Result<>.Result(T0)|method|Result<>|Shop.Domain/Result.cs|true",
		"cs:src/Shop.Domain:Result<>.Value|value|Result<>|Shop.Domain/Result.cs|true",
		"cs:src/Shop.Web:<Program>|function||Shop.Web/Program.cs|false",
	}

	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("symbols:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestFixtureSymbolDetails(t *testing.T) {
	_, g := extractShop(t)
	product := symbolOf(t, g, "cs:src/Shop.Domain/Catalog:Product")
	if product.Signature != "public sealed partial class Product : IComparable<Product>" {
		t.Errorf("partial signature: %q", product.Signature)
	}

	price := symbolOf(t, g, "cs:src/Shop.Domain/Catalog:Product.Price()")
	if len(price.Variants) != 0 || price.File != "src/Shop.Domain/Catalog/Taxes/Product.Pricing.cs" || price.Line != 5 || price.Signature != "public partial decimal Price()" {
		t.Errorf("partial method: file %s line %d signature %q variants %d", price.File, price.Line, price.Signature, len(price.Variants))
	}

	if price.Doc != "The price after discounts." || price.DocLine != 0 {
		t.Errorf("partial method doc: %q at %d", price.Doc, price.DocLine)
	}

	describe := symbolOf(t, g, "cs:src/Shop.Domain:Order.Describe()")
	if len(describe.Variants) != 1 || describe.Variants[0].Signature != describe.Signature {
		t.Errorf("#if member: %d variants", len(describe.Variants))
	}

	for _, symbol := range g.Symbols {
		if len(symbol.Fields) > 0 {
			t.Errorf("%s has fields", symbol.ID)
		}
	}

	order := symbolOf(t, g, "cs:src/Shop.Domain:Order")
	if order.Doc != "An order a customer places." || order.DocLine != 5 {
		t.Errorf("doc: %q at %d", order.Doc, order.DocLine)
	}
}

func TestPartialVisibility(t *testing.T) {
	_, g := extractShop(t)
	if product := symbolOf(t, g, "cs:src/Shop.Domain/Catalog:Product"); !product.Contract || !strings.HasPrefix(product.Signature, "public ") {
		t.Errorf("Product: contract %v signature %q", product.Contract, product.Signature)
	}
}

func TestTypeHash(t *testing.T) {
	hashes := func(change func(root string)) (store, catalog string) {
		root := shopCopy(t)
		change(root)
		g, err := NewExtractor().Extract(root)
		if err != nil {
			t.Fatal(err)
		}

		return symbolOf(t, g, "cs:src/Shop.Adapters:SqlOrderStore").Hash, symbolOf(t, g, "cs:src/Shop.Adapters:SqlOrderStore.Catalog()").Hash
	}

	edit := func(old, new string) func(root string) {
		return func(root string) {
			file := filepath.Join(root, "src", "Shop.Adapters", "SqlOrderStore.cs")
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(file, []byte(strings.Replace(string(content), old, new, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	baseStore, baseCatalog := hashes(func(string) {})
	if store, _ := hashes(edit("Retry.Times(3);", "Retry.Times(4);")); store == baseStore {
		t.Errorf("a static constructor body change kept the type's hash")
	}

	if store, catalog := hashes(edit("Catalog() => new();", "Catalog() => new(4);")); store != baseStore || catalog == baseCatalog {
		t.Errorf("a method body change: type hash changed %v, method hash changed %v", store != baseStore, catalog != baseCatalog)
	}
}

// edgeList writes each edge as from -> to (kind), without the language
// prefix.
func edgeList(g *graph.Graph) []string {
	var result []string
	for _, edge := range g.Edges {
		result = append(result, fmt.Sprintf("%s -> %s (%s)", strings.TrimPrefix(edge.From, "cs:"), strings.TrimPrefix(edge.To, "cs:"), edge.Kind))
	}

	return result
}

// hasEdges checks that every wanted edge is present and no unwanted one is.
func hasEdges(t *testing.T, g *graph.Graph, want, unwanted []string) {
	t.Helper()
	got := edgeList(g)
	for _, each := range want {
		if !slices.Contains(got, each) {
			t.Errorf("missing edge %s", each)
		}
	}

	for _, each := range unwanted {
		if slices.Contains(got, each) {
			t.Errorf("unwanted edge %s", each)
		}
	}

	if t.Failed() {
		t.Logf("edges:\n  %s", strings.Join(got, "\n  "))
	}
}

func TestFixtureEdges(t *testing.T) {
	_, g := extractShop(t)
	hasEdges(t, g, []string{
		// A type in a signature.
		"src/Shop.App:PlaceOrder.Run(int) -> src/Shop.Domain:Result<> (type-use)",
		"src/Shop.App:PlaceOrder.Run(int) -> src/Shop.Domain:Order (type-use)",
		// A base class and an interface.
		"src/Shop.Adapters:CachedOrderStore -> src/Shop.Adapters:SqlOrderStore (embeds)",
		"src/Shop.Adapters:SqlOrderStore -> src/Shop.Domain:IOrderStore (implements)",
		// T.M(args) with a known type.
		"src/Shop.Domain/Catalog:Product.Price() -> src/Shop.Domain:Pricing.Discount(decimal) (call)",
		// x.M(args) with an unknown x.
		"src/Shop.Web:<Program> -> src/Shop.App:PlaceOrder.Run(int) (call)",
		// M(args) inside a type.
		"src/Shop.Domain:Order.Add(Line,int) -> src/Shop.Domain:Order.Add(Line) (call)",
		// A qualified name.
		"src/Shop.Adapters:CachedOrderStore.Cut() -> src/Shop.Domain:Pricing.Discount(decimal) (call)",
		// An alias.
		"src/Shop.Web:<Program> -> src/Shop.Domain:IOrderStore (type-use)",
		// A using static.
		"src/Shop.App:PlaceOrder.Run(int) -> src/Shop.Domain:Pricing.Discount(decimal) (call)",
	}, []string{
		// x.M(args) with two reachable methods named Save.
		"src/Shop.App:PlaceOrder.Run(int) -> src/Shop.Domain:IOrderStore.Save(Order) (call)",
		"src/Shop.App:PlaceOrder.Run(int) -> src/Shop.Adapters:SqlOrderStore.Save(Order) (call)",
	})

	var imports []string
	for _, each := range g.Imports {
		imports = append(imports, fmt.Sprintf("%s -> %s", each.From, each.To))
	}

	sort.Strings(imports)
	if want := []string{"cs:src/Shop.App -> cs:src/Shop.Domain", "cs:src/Shop.Web -> cs:src/Shop.Domain"}; !slices.Equal(imports, want) {
		t.Errorf("imports: got %q, want %q", imports, want)
	}

	for _, edge := range g.Edges {
		if strings.Contains(edge.To, "List") || strings.Contains(edge.To, "Dictionary") {
			t.Errorf("edge to a name outside the repository: %s -> %s", edge.From, edge.To)
		}
	}

	hasEdges(t, g, []string{
		// The program builds its types.
		"src/Shop.Web:<Program> -> src/Shop.Adapters:SqlOrderStore (type-use)",
		"src/Shop.Web:<Program> -> src/Shop.App:PlaceOrder (type-use)",
		// A static constructor and an explicit implementation belong to the type.
		"src/Shop.Adapters:SqlOrderStore -> src/Shop.Adapters:Retry.Times(int) (call)",
		"src/Shop.Adapters:SqlOrderStore -> src/Shop.Adapters:SqlOrderStore.Catalog() (call)",
		// A type that only a <Using> item of the project file brings in.
		"src/Shop.Adapters:SqlOrderStore.Catalog() -> src/Shop.Domain/Catalog:Product (type-use)",
	}, nil)
}

// extractTree writes files under a new directory and extracts them.
func extractTree(t *testing.T, files map[string]string) *graph.Graph {
	t.Helper()
	g, err := NewExtractor().Extract(writeTree(t, files))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	return g
}

func TestResolution(t *testing.T) {
	library := map[string]string{
		"A/A.csproj": sdkProject(""),
		"A/X/One.cs": "namespace Lib;\npublic class One { }",
		"A/Y/Two.cs": "namespace Lib;\npublic class Two { }",
		"A/S/Calls.cs": join(
			"namespace Lib;",
			"public static class S {",
			"    public static void M(int a) { }",
			"    public static void M(string a) { }",
			"}",
			"public static class S2 {",
			"    public static void M(object a) { }",
			"    public static void M(object a, object b) { }",
			"}",
			"public static class Ext {",
			"    public static void Ext1(this One one, int n) { }",
			"}",
			"public class G1 { public void Go() { } }",
			"public class G2 { public void Go() { } }",
		),
	}

	with := func(name, src string) map[string]string {
		result := maps.Clone(library)
		result[name] = src
		return result
	}

	for _, each := range []struct {
		name     string
		files    map[string]string
		want     []string
		unwanted []string
	}{
		{"one namespace in two directories", with("A/Z/Use.cs", "using Lib;\nnamespace App;\nclass Use { Two two; One one; }"),
			[]string{"A/Z:Use.two -> A/Y:Two (type-use)", "A/Z:Use.one -> A/X:One (type-use)"}, nil},
		{"alias wins over a using", with("A/Z/Use.cs", "using Lib;\nusing One = Lib.Two;\nnamespace App;\nclass Use { One x; }"),
			[]string{"A/Z:Use.x -> A/Y:Two (type-use)"}, []string{"A/Z:Use.x -> A/X:One (type-use)"}},
		{"alias in a namespace block", with("A/Z/Use.cs", "namespace App {\n    using T = Lib.Two;\n    class Use { T x; }\n}"),
			[]string{"A/Z:Use.x -> A/Y:Two (type-use)"}, nil},
		{"overloads with the same count", with("A/Z/Use.cs", "using Lib;\nclass Use { void Run() { S.M(1); } }"),
			[]string{"A/Z:Use.Run() -> A/S:S (type-use)"}, []string{"A/Z:Use.Run() -> A/S:S.M(int) (call)", "A/Z:Use.Run() -> A/S:S.M(string) (call)"}},
		{"type arguments hide commas", with("A/Z/Use.cs", "using Lib;\nclass Use { void Run() { S2.M(new Dictionary<string,int>()); } }"),
			[]string{"A/Z:Use.Run() -> A/S:S2.M(object) (call)"}, nil},
		{"comparisons are two arguments", with("A/Z/Use.cs", "using Lib;\nclass Use { void Run(int a, int b, int c, int d) { S2.M(a < b, c > d); } }"),
			[]string{"A/Z:Use.Run(int,int,int,int) -> A/S:S2.M(object,object) (call)"}, nil},
		{"a generic call is one argument", with("A/Z/Use.cs", "using Lib;\nclass Use { void Run(int x, int y) { S2.M(F<int>(x), y); } }"),
			[]string{"A/Z:Use.Run(int,int) -> A/S:S2.M(object,object) (call)"}, nil},
		{"extension method", with("A/Z/Use.cs", "using Lib;\nclass Use { void Run(One one) { one.Ext1(1); } }"),
			[]string{"A/Z:Use.Run(One) -> A/S:Ext.Ext1(One,int) (call)"}, nil},
		{"two reachable methods", with("A/Z/Use.cs", "using Lib;\nclass Use { void Run(object x) { x.Go(); } }"),
			nil, []string{"A/Z:Use.Run(object) -> A/S:G1.Go() (call)", "A/Z:Use.Run(object) -> A/S:G2.Go() (call)"}},
		{"initializer member names", with("A/Z/Use.cs", join(
			"using Lib;",
			"record Box(int One, int Two);",
			"class Use {",
			"    Box Copy(Box b) => b with { One = 1, Two = 2 };",
			"    Box Make() => new Box(0, 0) { One = 1 };",
			"    void Set(int One) { One = 2; }",
			"}",
		)), []string{"A/Z:Use.Copy(Box) -> A/Z:Box (type-use)"}, []string{
			"A/Z:Use.Copy(Box) -> A/X:One (type-use)", "A/Z:Use.Copy(Box) -> A/Y:Two (type-use)", "A/Z:Use.Make() -> A/X:One (type-use)",
		}},
	} {
		t.Run(each.name, func(t *testing.T) {
			hasEdges(t, extractTree(t, each.files), each.want, each.unwanted)
		})
	}

	t.Run("unreferenced project", func(t *testing.T) {
		g := extractTree(t, map[string]string{
			"P1/P1.csproj": sdkProject(""),
			"P1/Thing.cs":  "namespace P1;\npublic class Thing { }",
			"P2/P2.csproj": sdkProject(""),
			"P2/User.cs":   "using P1;\nnamespace P2;\nclass User { Thing thing; }",
		})

		if len(g.Edges) != 0 {
			t.Errorf("edges: %q", edgeList(g))
		}
	})

	t.Run("unused using", func(t *testing.T) {
		g := extractTree(t, with("B/Use.cs", "using Lib;\nnamespace App;\nclass Use { }"))
		for _, edge := range g.Edges {
			if strings.HasPrefix(edge.From, "cs:B:") {
				t.Errorf("edge %s -> %s", edge.From, edge.To)
			}
		}

		if len(g.Imports) != 0 {
			t.Errorf("imports: %d", len(g.Imports))
		}
	})
}

func TestDialect(t *testing.T) {
	dialect := NewDialect()
	for text, want := range map[string]app.Declaration{
		"public class Order":                  {Kind: graph.KindType, Name: "Order", Contract: true},
		"internal sealed class Helper":        {Kind: graph.KindType, Name: "Helper", Contract: true},
		"file class Hidden":                   {Kind: graph.KindType, Name: "Hidden"},
		"public interface IStore":             {Kind: graph.KindInterface, Name: "IStore", Contract: true},
		"public record Point":                 {Kind: graph.KindType, Name: "Point", Contract: true},
		"public delegate void Handler(int x)": {Kind: graph.KindType, Name: "Handler", Contract: true},
	} {
		got, err := dialect.Declare(text, nil)
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}

		if got.Kind != want.Kind || got.Name != want.Name || got.Contract != want.Contract || !strings.HasPrefix(got.Key, "kind=") {
			t.Errorf("%q: got %+v, want %+v", text, got, want)
		}
	}
}

// declareUnder declares a line under the declarations of parent lines,
// outermost first.
func declareUnder(t *testing.T, text string, parents ...string) (app.Declaration, error) {
	t.Helper()
	dialect := NewDialect()
	var parent *app.Declaration
	for _, line := range parents {
		declared, err := dialect.Declare(line, parent)
		if err != nil {
			t.Fatalf("parent %q: %v", line, err)
		}

		parent = &declared
	}

	return dialect.Declare(text, parent)
}

func TestDialectMembers(t *testing.T) {
	for text, want := range map[string]string{
		"public class Line":                                                   "type Line true",
		"public void Add(Line line, int count)":                               "method Add(Line,int) true",
		"public void Process(Outer.Inner x)":                                  "method Process(Inner) true",
		"public void Log(String s)":                                           "method Log(string) true",
		"public Order(int id)":                                                "method Order(int) true",
		"public Line this[int i] { get; }":                                    "value this[int] true",
		"public T Map<T>(Func<T> f)":                                          "method Map<>(Func<M0>) true",
		"public void Save(ref int x, out int y, in int z, params int[] rest)": "method Save(int&,int&,int&,int[]) true",
		"public int Count { get; set; }":                                      "value Count true",
		"public event EventHandler Changed":                                   "value Changed true",
		"public const int Limit = 10":                                         "value Limit true",
		"public static readonly Money Zero = new Money()":                     "value Zero true",
		"public int count":                                                    "value count true",
		"private int @class":                                                  "value class false",
		"public static Money operator +(Money a, Money b)":                    "method op_Addition(Money,Money) true",
		"public static implicit operator Money(int x)":                        "method op_Implicit(int)Money true",
	} {
		got, err := declareUnder(t, text, "public class Order")
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}

		if line := fmt.Sprintf("%s %s %v", got.Kind, got.Name, got.Contract); line != want {
			t.Errorf("%q: got %s, want %s", text, line, want)
		}
	}
}

func TestDialectContext(t *testing.T) {
	dialect := NewDialect()
	color, err := dialect.Declare("public enum Color", nil)
	if err != nil || !strings.HasPrefix(color.Key, "kind=enum;") {
		t.Errorf("enum key %q, %v", color.Key, err)
	}

	if red, err := declareUnder(t, "Red = 1", "public enum Color"); err != nil || red.Name != "Red" || red.Kind != graph.KindValue || !red.Contract {
		t.Errorf("enum member: %+v, %v", red, err)
	}

	if _, err := declareUnder(t, "Red = 1", "public class Color"); !errors.Is(err, ErrNotDeclaration) {
		t.Errorf("enum member under a class: %v", err)
	}

	if program, err := dialect.Declare("<Program>", nil); err != nil || program.Name != "<Program>" || program.Kind != graph.KindFunction || program.Contract {
		t.Errorf("program: %+v, %v", program, err)
	}

	if result, err := dialect.Declare("public class Result<TValue>", nil); err != nil || !strings.Contains(result.Key, "tp=TValue;") {
		t.Errorf("generic key: %q, %v", result.Key, err)
	}

	for text, want := range map[string]string{"public TValue Get(int i)": "Get(int)", "public void Set(TValue v)": "Set(T0)"} {
		if got, err := declareUnder(t, text, "public class Result<TValue>"); err != nil || got.Name != want {
			t.Errorf("%q: got %q, %v", text, got.Name, err)
		}
	}

	if got, err := declareUnder(t, "public void M(TValue a, U b)", "public class Result<TValue>", "public class Inner<U>"); err != nil || got.Name != "M(T0,T1)" {
		t.Errorf("nested generic member: %q, %v", got.Name, err)
	}

	if got, err := declareUnder(t, "public Line(int n)", "public class Line"); err != nil || got.Name != "Line(int)" || got.Kind != graph.KindMethod {
		t.Errorf("constructor: %+v, %v", got, err)
	}

	for _, text := range []string{"return x;", "x + 1", "", "using System;"} {
		if _, err := dialect.Declare(text, nil); !errors.Is(err, ErrNotDeclaration) {
			t.Errorf("%q: got %v, want ErrNotDeclaration", text, err)
		}
	}

	first, _ := declareUnder(t, "public void Add(Line line)", "public class Order")
	second, _ := declareUnder(t, "public void Add(Line other)", "public class Order")
	if first.Key != second.Key || first.Key == "" {
		t.Errorf("renamed parameter: %q and %q", first.Key, second.Key)
	}
}

// compatibleCase is one signature change and whether callers survive it.
type compatibleCase struct {
	kind       string
	parent     string
	before     string
	after      string
	compatible bool
}

var compatibleCases = []compatibleCase{
	{graph.KindMethod, "Order", "public void Add(Line line)", "public void Add(Line other)", true},
	{graph.KindMethod, "Order", "public void Add(Line line)", "public static void Add(Line line)", false},
	{graph.KindValue, "Order", "public const int Limit = 10", "public  const int Limit =  10", true},
	{graph.KindValue, "Order", "public const int Limit = 10", "public const int Limit = 11", false},
	{graph.KindValue, "Color", "public Red = 1", "public Red = 2", false},
	{graph.KindValue, "Order", "public static readonly Money Zero = new Money()", "public static readonly Money Zero = Money.Create()", true},
	{graph.KindValue, "Order", "public int count", "public long count", false},
	{graph.KindValue, "Order", "public int Count { get; }", "public int Count { get; set; }", true},
	{graph.KindValue, "Order", "public int Count { get; set; }", "public int Count { get; }", false},
	{graph.KindValue, "Order", "public event EventHandler Changed", "public  event EventHandler Changed", true},
	{graph.KindValue, "Order", "public event EventHandler Changed", "public event Action Changed", false},
	{graph.KindType, "", "public class A", "public class A : IFoo", true},
	{graph.KindType, "", "public class A", "public static class A", false},
	{graph.KindInterface, "", "public interface I<T>", "public interface I<U>", true},
	{graph.KindInterface, "", "public interface I<T>", "internal interface I<T>", false},
	// The type key.
	{graph.KindType, "", "public class A : IFoo", "public class A : IFoo, IBar", true},
	{graph.KindType, "", "public class A : IFoo", "public class A : Base, IFoo", true},
	{graph.KindType, "", "public class A : Base", "public class A : Other", false},
	{graph.KindType, "", "public sealed class A", "public class A", true},
	{graph.KindType, "", "public class A", "public sealed class A", false},
	{graph.KindType, "", "public class Box<T>", "public class Box<TItem>", true},
	{graph.KindType, "", "public class Service", "public class Service", true},
	// The interface key.
	{graph.KindInterface, "", "public interface IStore", "public interface IStore : IDisposable", false},
	{graph.KindInterface, "", "public interface IStore<T> where T : class", "public interface IStore<TItem> where TItem : class", true},
	// Type parameters.
	{graph.KindMethod, "Order", "public T Map<T>(Func<T> f)", "public TOut Map<TOut>(Func<TOut> f)", true},
	{graph.KindMethod, "Box<>", "public T Get()", "public TValue Get()", false},
	// Properties.
	{graph.KindValue, "Order", "public int Id { get; set; }", "public int Id { get; init; }", false},
	{graph.KindValue, "Order", "public int Id { get; init; }", "public int Id { get; set; }", false},
	{graph.KindValue, "Order", "public int Id { get; set; }", "public int Id { get; private set; }", false},
	{graph.KindValue, "Order", "public string Name { get; set; }", "public required string Name { get; set; }", false},
	{graph.KindValue, "Order", "public required string Name { get; set; }", "public string Name { get; set; }", true},
	// Methods.
	{graph.KindMethod, "Order", "public void Ship(int days = 1)", "public void Ship(int days = 2)", false},
	{graph.KindMethod, "Order", "public void Ship(Outer.Inner x)", "public void Ship(Other.Inner x)", false},
	{graph.KindMethod, "Order", "public void Log(String s)", "public void Log(string s)", true},
}

func TestCompatible(t *testing.T) {
	dialect := NewDialect()
	for _, each := range compatibleCases {
		before := &graph.Symbol{Kind: each.kind, Parent: each.parent, Signature: each.before}
		after := &graph.Symbol{Kind: each.kind, Parent: each.parent, Signature: each.after}
		if got := dialect.Compatible(before, after); got != each.compatible {
			t.Errorf("%q -> %q: compatible %v, want %v", each.before, each.after, got, each.compatible)
		}
	}
}

// newService builds a service over root with the C# adapter, and git when
// root is a repository.
func newService(t *testing.T, root string) *app.Service {
	t.Helper()
	var history app.VersionControl
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		history = gitvcs.New(root)
	}

	return app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		history, filesystem.NewWorkspace(t.TempDir()), nil, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
}

func TestRoundTrip(t *testing.T) {
	root := shopCopy(t)
	if err := os.Remove(filepath.Join(root, "src", "Shop.Domain", "Rules", "Order.Validation.cs")); err != nil {
		t.Fatal(err)
	}

	service := newService(t, root)
	dumped, err := service.Dump("")
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := service.ParseGraph(dumped)
	if err != nil {
		t.Fatalf("ParseGraph: %v\n%s", err, dumped)
	}

	extracted, _ := service.Extract()
	if differences := graph.Compare(extracted, parsed); len(differences) > 0 {
		t.Fatalf("round trip lost %d thing(s):\n%s", len(differences), strings.Join(differences[:min(len(differences), 30)], "\n"))
	}
}

func TestFixtureCheck(t *testing.T) {
	report, err := newService(t, shopCopy(t)).Check("")
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, violation := range report.Violations {
		violations = append(violations, violation.From+" -> "+violation.To)
	}

	if want := []string{"cs:src/Shop.App -> cs:src/Shop.Adapters"}; !slices.Equal(violations, want) {
		t.Errorf("violations: got %q, want %q", violations, want)
	}

	for _, finding := range report.Findings {
		if finding.Kind == rules.FindingVariants || strings.Contains(finding.Title, "Build variants disagree") {
			t.Errorf("finding: %s", finding.Title)
		}
	}
}

// breakingCase is one edit to the fixture and the findings it gives for a
// symbol.
type breakingCase struct {
	name   string
	file   string
	edits  []string
	target string
	want   string
}

const (
	wantBreaking     = "breaking"
	wantCompatible   = "compatible"
	wantImplementers = "implementers"
	wantNoBreaking   = "no breaking"
)

var breakingCases = []breakingCase{
	{"changed parameter type", "Shop.Domain/Pricing.cs", []string{"Discount(decimal amount) => amount * 0.9m", "Discount(double amount) => (decimal)amount * 0.9m"},
		"cs:src/Shop.Domain:Pricing.Discount(decimal)", wantBreaking},
	{"renamed parameter", "Shop.Domain/Pricing.cs", []string{"Discount(decimal amount) => amount * 0.9m", "Discount(decimal value) => value * 0.9m"},
		"cs:src/Shop.Domain:Pricing.Discount(decimal)", wantCompatible},
	{"renamed primary constructor parameter", "Shop.App/PlaceOrder.cs", []string{"PlaceOrder(IOrderStore store)", "PlaceOrder(IOrderStore orders)", "store.Save(order)", "orders.Save(order)"},
		"cs:src/Shop.App:PlaceOrder", wantNoBreaking},
	{"renamed type parameter", "Shop.Domain/Result.cs", []string{"class Result<T>", "class Result<TValue>", "Result(T value)", "Result(TValue value)", "public T Value", "public TValue Value", "Func<T, TOut>", "Func<TValue, TOut>"},
		"cs:src/Shop.Domain:Result<>.Value", wantBreaking},
	{"new interface property", "Shop.Domain/IOrderStore.cs", []string{"int Count { get; }", "int Count { get; }\n\n    string Name { get; }"},
		"cs:src/Shop.Domain:IOrderStore", wantImplementers},
	{"new setter", "Shop.Domain/Result.cs", []string{"public T Value { get; }", "public T Value { get; set; }"},
		"cs:src/Shop.Domain:Result<>.Value", wantNoBreaking},
	{"changed const value", "Shop.Domain/Order.cs", []string{"Limit = 10", "Limit = 11"},
		"cs:src/Shop.Domain:Order.Limit", wantBreaking},
	{"changed enum value", "Shop.Domain/OrderStatus.cs", []string{"    New,", "    New = 5,"},
		"cs:src/Shop.Domain:OrderStatus.New", wantBreaking},
	{"new enum member at the end", "Shop.Domain/OrderStatus.cs", []string{"    Shipped,", "    Shipped,\n    Cancelled,"},
		"cs:src/Shop.Domain:OrderStatus.Shipped", wantNoBreaking},
	{"new enum member before implicit values", "Shop.Domain/OrderStatus.cs", []string{"    New,", "    Draft,\n    New,"},
		"cs:src/Shop.Domain:OrderStatus.New", wantBreaking},
	{"changed static readonly initializer", "Shop.Domain/Order.cs", []string{"Empty = new Order(0)", "Empty = new Order(1)"},
		"cs:src/Shop.Domain:Order.Empty", wantNoBreaking},
	{"added interface", "Shop.Adapters/CachedOrderStore.cs", []string{"CachedOrderStore : SqlOrderStore", "CachedOrderStore : SqlOrderStore, IDisposable"},
		"cs:src/Shop.Adapters:CachedOrderStore", wantNoBreaking},
	{"added required", "Shop.Domain/Order.cs", []string{"public decimal Price { get; set; }", "public required decimal Price { get; set; }"},
		"cs:src/Shop.Domain:Order.Line.Price", wantBreaking},
}

func TestBreakingChanges(t *testing.T) {
	for _, each := range breakingCases {
		t.Run(each.name, func(t *testing.T) {
			root := shopCopy(t)
			git := func(args ...string) {
				if output, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, output)
				}
			}

			git("init", "-q")
			git("add", ".")
			git("commit", "-q", "-m", "base")
			file := filepath.Join(root, "src", filepath.FromSlash(each.file))
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}

			text := string(content)
			for index := 0; index+1 < len(each.edits); index += 2 {
				if !strings.Contains(text, each.edits[index]) {
					t.Fatalf("%s has no %q", each.file, each.edits[index])
				}

				text = strings.ReplaceAll(text, each.edits[index], each.edits[index+1])
			}

			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}

			report, err := newService(t, root).Check("HEAD")
			if err != nil {
				t.Fatal(err)
			}

			kinds := map[string]bool{}
			for _, finding := range report.Findings {
				if slices.Contains(finding.Targets, each.target) {
					kinds[finding.Kind] = true
				}
			}

			switch each.want {
			case wantBreaking, wantImplementers:
				if !kinds[each.want] {
					t.Errorf("%s: findings %v, want %s", each.target, kinds, each.want)
				}
			case wantCompatible:
				if !kinds[rules.FindingCompatible] || kinds[rules.FindingBreaking] {
					t.Errorf("%s: findings %v, want compatible and no breaking", each.target, kinds)
				}
			case wantNoBreaking:
				if kinds[rules.FindingBreaking] {
					t.Errorf("%s: findings %v, want no breaking", each.target, kinds)
				}
			}
		})
	}
}

// designService builds a service over a fresh copy of the fixture, with a
// workspace that holds the given designs.
func designService(t *testing.T, designs map[string]string) (*app.Service, string) {
	t.Helper()
	root, workspace := shopCopy(t), t.TempDir()
	for name, text := range designs {
		file := filepath.Join(workspace, ".treaty", "designs", name+".pen")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := app.NewService(root, filesystem.NewConfig(root), NewExtractor(), []app.Dialect{NewDialect()},
		nil, filesystem.NewWorkspace(workspace), nil, filesystem.NewAgentConfig(t.TempDir()), filesystem.NewThemes(""), filesystem.NewPreferences(""))
	return service, workspace
}

func TestDesignNested(t *testing.T) {
	service, _ := designService(t, map[string]string{
		"nested": join(
			"autopen 1",
			`design "Nested"`,
			"",
			"cs:src/Shop.Domain/Order.cs",
			"  public class Order",
			"    public class Line",
			"      public decimal Total(int count)",
			"",
		),
		"unbuilt": join(
			"autopen 1",
			`design "Unbuilt"`,
			"",
			"cs:src/Shop.Domain/Order.cs",
			"  public class Order",
			"    public class Missing",
			"      public void Run()",
			"",
		),
	})

	report, err := service.DesignCheck("nested")
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range report.Items {
		if item.Kind == app.ItemMissing {
			t.Errorf("nested member: %s %s: %s", item.Kind, item.Target, item.Detail)
		}
	}

	report, err = service.DesignCheck("unbuilt")
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for _, item := range report.Items {
		if item.Kind == app.ItemMissing {
			missing = append(missing, item.Target)
		}
	}

	if !slices.Contains(missing, "cs:src/Shop.Domain:Order.Missing") {
		t.Errorf("unbuilt nested type: missing %q", missing)
	}
}

func TestDesignNewNested(t *testing.T) {
	service, _ := designService(t, nil)
	file, err := service.DesignNew("scaffold", []string{"cs:src/Shop.Domain:Order.Line.Total(int)"})
	if err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	want := "  public class Order\n    public class Line\n      public decimal Total(int count)\n"
	if !strings.Contains(string(content), want) {
		t.Errorf("scaffold:\n%s\nwant it to hold:\n%s", content, want)
	}
}

// TestRealSolution reads a real C# solution and logs what it found. It runs
// only when TREATY_CSHARP_SOLUTION names a directory.
func TestRealSolution(t *testing.T) {
	root := os.Getenv("TREATY_CSHARP_SOLUTION")
	if root == "" {
		t.Skip("TREATY_CSHARP_SOLUTION is not set")
	}

	extractor := NewExtractor()
	g, err := extractor.Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("modules %d, symbols %d, edges %d, imports %d, skipped %d", len(g.Modules), len(g.Symbols), len(g.Edges), len(g.Imports), len(extractor.Skipped()))
	for _, each := range extractor.Skipped() {
		t.Logf("skipped %s: %s", each.path, each.reason)
	}
}

func TestNominalTypes(t *testing.T) {
	_, g := extractShop(t)
	dialect := NewDialect()

	// SqlOrderStore names IOrderStore, so an implements edge records it; no
	// structural match is needed, and none is made for a type that does not.
	if dialect.Satisfies(g, "cs:src/Shop.Adapters:SqlOrderStore", "cs:src/Shop.Domain:IOrderStore") || dialect.Satisfies(g, "cs:src/Shop.Domain:Pricing", "cs:src/Shop.Domain:IOrderStore") {
		t.Errorf("Satisfies matched structurally")
	}

	// using Store = Shop.Domain.IOrderStore is file-scoped and makes no symbol.
	for _, symbol := range g.Symbols {
		if target := dialect.Alias(g, symbol.ID); target != "" {
			t.Errorf("%s aliases %s", symbol.ID, target)
		}
	}
}

// symbolOf finds a symbol or fails the test.
func symbolOf(t *testing.T, g *graph.Graph, id string) *graph.Symbol {
	t.Helper()
	symbol := g.Symbol(id)
	if symbol == nil {
		t.Fatalf("no symbol %s", id)
	}

	return symbol
}

// shopCopy copies the shop fixture to a new directory and adds the build
// output directories that git does not keep.
func shopCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir("testdata/shop", func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, _ := filepath.Rel("testdata/shop", current)
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		content, err := os.ReadFile(current)
		if err != nil {
			return err
		}

		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"src/Shop.Domain/bin/Debug/Built.cs", "src/Shop.Domain/obj/Debug/Shop.Domain.AssemblyInfo.cs"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte("namespace Shop.Domain;\npublic class BuildOutput { }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func texts(tokens []token) []string {
	var result []string
	for _, each := range tokens {
		result = append(result, each.text)
	}

	return result
}

// describe writes a declaration as kind|name|parent|signature.
func describe(declaration *declaration) string {
	parent := ""
	if declaration.owner != nil {
		parent = declaration.owner.name
	}

	return declaration.kind + "|" + declaration.name + "|" + parent + "|" + declaration.signature
}

// join is a helper for multi-line sources in test tables.
func join(lines ...string) string {
	return strings.Join(lines, "\n")
}

func TestDialectPaths(t *testing.T) {
	dialect := NewDialect()
	if got := dialect.Language(); got != "cs" {
		t.Errorf("Language: got %q, want cs", got)
	}

	if !dialect.IsFile("a.cs") || dialect.IsFile("a.go") {
		t.Errorf("IsFile: got %v for a.cs and %v for a.go", dialect.IsFile("a.cs"), dialect.IsFile("a.go"))
	}

	for name, want := range map[string]string{"src/App/Order.cs": "src/App", "src/App": "src/App"} {
		if got := dialect.ModulePath(name); got != want {
			t.Errorf("ModulePath(%q): got %q, want %q", name, got, want)
		}
	}
}
