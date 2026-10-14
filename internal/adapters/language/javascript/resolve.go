package javascript

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	bundlerConfigs   = []string{"vite.config.ts", "vite.config.mts", "vite.config.js", "vite.config.mjs", "vite.config.cjs", "webpack.config.js", "webpack.config.ts", "webpack.config.cjs", "webpack.config.mjs"}
	sourceExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
	trailingComma    = regexp.MustCompile(`,(\s*[}\]])`)
	typeScriptTwins  = map[string][]string{".js": {".ts", ".tsx"}, ".jsx": {".tsx"}, ".mjs": {".mts"}, ".cjs": {".cts"}}
)

// compilerConfig is the module resolution a tsconfig.json or jsconfig.json,
// and a bundler config's aliases, set up for the files beneath them.
type compilerConfig struct {
	baseURL string
	paths   []pathRule
}

// manifest is one package.json: a package other files can import by name.
type manifest struct {
	dir     string
	name    string
	private bool
	entries []string
	imports []pathRule
}

// pathRule is one compilerOptions.paths entry, such as "@/*": ["src/*"].
type pathRule struct {
	prefix   string
	suffix   string
	wildcard bool
	targets  []string
}

// resolver maps import specifiers to source files in the tree. Anything it
// cannot place in the tree, such as a package from node_modules, is outside
// the repository.
type resolver struct {
	files    map[string]bool
	byDir    map[string]*manifest
	packages []*manifest
	configs  map[string]*compilerConfig
}

// newResolver indexes the tree's source files, packages and compiler
// configs.
func newResolver(files []string, manifests map[string]*manifest, configs map[string]*compilerConfig) *resolver {
	result := &resolver{files: map[string]bool{}, byDir: manifests, configs: configs}
	for _, file := range files {
		result.files[file] = true
	}

	for _, each := range manifests {
		if each.name != "" {
			result.packages = append(result.packages, each)
		}
	}

	sort.Slice(result.packages, func(i, j int) bool {
		a, b := result.packages[i], result.packages[j]
		if len(a.name) != len(b.name) {
			return len(a.name) > len(b.name)
		}

		return a.dir < b.dir
	})
	return result
}

// config finds the compiler config nearest above a directory.
func (this *resolver) config(dir string) *compilerConfig {
	for {
		if found := this.configs[dir]; found != nil {
			return found
		}

		if dir == "." || dir == "" {
			return nil
		}

		dir = path.Dir(dir)
	}
}

// entry finds a package's main source file: a field of its package.json,
// else its index, else src/index.
func (this *resolver) entry(each *manifest) (string, bool) {
	for _, candidate := range each.entries {
		if found, ok := this.probeFile(path.Join(each.dir, candidate)); ok {
			return found, true
		}
	}

	if found, ok := this.probeFile(each.dir); ok {
		return found, true
	}

	return this.probeFile(path.Join(each.dir, "src"))
}

// probe finds the source file a path names, trying TypeScript twins of
// .js paths, every source extension, a package's entry and an index file.
func (this *resolver) probe(name string) (string, bool) {
	if found, ok := this.probeFile(name); ok {
		return found, true
	}

	if each := this.byDir[path.Clean(name)]; each != nil {
		return this.entry(each)
	}

	return "", false
}

func (this *resolver) probeFile(name string) (string, bool) {
	name = path.Clean(name)
	if name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
		return "", false
	}

	if this.files[name] {
		return name, true
	}

	extension := path.Ext(name)
	for _, twin := range typeScriptTwins[extension] {
		if candidate := strings.TrimSuffix(name, extension) + twin; this.files[candidate] {
			return candidate, true
		}
	}

	for _, each := range sourceExtensions {
		if this.files[name+each] {
			return name + each, true
		}
	}

	for _, each := range sourceExtensions {
		if candidate := path.Join(name, "index"+each); this.files[candidate] {
			return candidate, true
		}
	}

	return "", false
}

// resolve finds the source file an import names: a relative path, a path
// alias or base URL from the nearest compiler config, or a package in the
// tree by its package.json name.
//
// Parameters:
//   - from: the importing file.
//   - specifier: what the import names.
//
// Returns:
//   - result: the imported file.
//   - found: false when the import leaves the tree.
func (this *resolver) resolve(from, specifier string) (result string, found bool) {
	dir := path.Dir(from)
	if strings.HasPrefix(specifier, "#") {
		return this.subpath(dir, specifier)
	}

	if specifier == "." || specifier == ".." || strings.HasPrefix(specifier, "./") || strings.HasPrefix(specifier, "../") {
		return this.probe(path.Join(dir, specifier))
	}

	if config := this.config(dir); config != nil {
		for _, rule := range config.paths {
			rest, ok := rule.match(specifier)
			if !ok {
				continue
			}

			for _, target := range rule.targets {
				if found, ok := this.probe(strings.Replace(target, "*", rest, 1)); ok {
					return found, true
				}
			}
		}

		if config.baseURL != "" {
			if found, ok := this.probe(path.Join(config.baseURL, specifier)); ok {
				return found, true
			}
		}
	}

	for _, each := range this.packages {
		if specifier == each.name {
			return this.entry(each)
		}

		if rest, ok := strings.CutPrefix(specifier, each.name+"/"); ok {
			return this.probe(path.Join(each.dir, rest))
		}
	}

	return "", false
}

// glob lists the source files a relative glob matches from the importing
// file, sorted: * and ? within a name, ** across directories, and {a,b}
// for alternatives.
func (this *resolver) glob(from, pattern string) (result []string) {
	if !strings.HasPrefix(pattern, "./") && !strings.HasPrefix(pattern, "../") {
		return nil
	}

	var expression strings.Builder
	expression.WriteString("^")
	full := path.Join(path.Dir(from), pattern)

	for index := 0; index < len(full); index++ {
		switch c := full[index]; {
		case strings.HasPrefix(full[index:], "**/"):
			expression.WriteString("(?:.*/)?")
			index += 2
		case strings.HasPrefix(full[index:], "**"):
			expression.WriteString(".*")
			index++
		case c == '*':
			expression.WriteString("[^/]*")
		case c == '?':
			expression.WriteString("[^/]")
		case c == '{':
			expression.WriteString("(?:")
		case c == '}':
			expression.WriteString(")")
		case c == ',' && strings.Contains(full[:index], "{"):
			expression.WriteString("|")
		default:
			expression.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	matcher, err := regexp.Compile(expression.String() + "$")
	if err != nil {
		return nil
	}

	for file := range this.files {
		if matcher.MatchString(file) {
			result = append(result, file)
		}
	}

	sort.Strings(result)
	return result
}

// subpath finds the file a package.json imports entry, such as
// "#internal/*", maps a specifier to, from the package nearest above dir.
func (this *resolver) subpath(dir, specifier string) (string, bool) {
	for {
		if each := this.byDir[dir]; each != nil {
			for _, rule := range each.imports {
				rest, ok := rule.match(specifier)
				if !ok {
					continue
				}

				for _, target := range rule.targets {
					if found, ok := this.probe(strings.Replace(target, "*", rest, 1)); ok {
						return found, true
					}
				}
			}

			return "", false
		}

		if dir == "." || dir == "" {
			return "", false
		}

		dir = path.Dir(dir)
	}
}

// match reports whether a specifier fits the rule, and what its wildcard
// stands for.
func (this pathRule) match(specifier string) (rest string, ok bool) {
	if !this.wildcard {
		return "", specifier == this.prefix
	}

	if len(specifier) < len(this.prefix)+len(this.suffix) || !strings.HasPrefix(specifier, this.prefix) || !strings.HasSuffix(specifier, this.suffix) {
		return "", false
	}

	return specifier[len(this.prefix) : len(specifier)-len(this.suffix)], true
}

// exportTargets lists the file paths a package.json exports field names
// for the package root, in the order a bundler would try them.
func exportTargets(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case map[string]any:
		if root, ok := typed["."]; ok {
			return exportTargets(root)
		}

		var result []string
		for _, condition := range []string{"source", "import", "module", "require", "node", "default"} {
			result = append(result, exportTargets(typed[condition])...)
		}

		return result
	default:
		return nil
	}
}

// readCompilerConfig reads the module resolution set up in a directory:
// the baseUrl and paths of its tsconfig.json or jsconfig.json, following
// extends and the configs its references name, as Vite's tsconfig.app.json
// is, and the aliases of its Vite or webpack config. It is nil when none of
// them sets any.
func readCompilerConfig(root, dir string) *compilerConfig {
	result := &compilerConfig{}
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		settings, ok := readTSConfig(root, path.Join(dir, name), 0)
		if !ok {
			continue
		}

		result.baseURL = settings.baseURL
		result.paths = settings.rules()
		for _, reference := range settings.references {
			if referenced, ok := readTSConfig(root, reference, 0); ok {
				if result.baseURL == "" {
					result.baseURL = referenced.baseURL
				}

				result.paths = append(result.paths, referenced.rules()...)
			}
		}

		break
	}

	result.paths = append(result.paths, readBundlerAliases(root, dir)...)
	if result.baseURL == "" && len(result.paths) == 0 {
		return nil
	}

	return result
}

// tsSettings is the module resolution one tsconfig file arrives at once
// what it extends is applied. Paths resolve from baseUrl when there is one,
// else from the directory of the config that set them.
type tsSettings struct {
	baseURL    string
	paths      map[string][]string
	pathsDir   string
	references []string
}

// readTSConfig reads a tsconfig file and the configs it extends, the later
// ones overriding the earlier and the file itself overriding them all.
func readTSConfig(root, file string, depth int) (result tsSettings, ok bool) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil || depth > 8 {
		return result, false
	}

	var parsed struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
		References []struct {
			Path string `json:"path"`
		} `json:"references"`
	}

	if json.Unmarshal(stripJSONComments(data), &parsed) != nil {
		return result, false
	}

	dir := path.Dir(file)
	var extends []string
	if json.Unmarshal(parsed.Extends, &extends) != nil {
		var single string
		if json.Unmarshal(parsed.Extends, &single) == nil && single != "" {
			extends = []string{single}
		}
	}

	for _, each := range extends {
		if parent, ok := readTSConfig(root, extendedConfig(root, dir, each), depth+1); ok {
			if parent.baseURL != "" {
				result.baseURL = parent.baseURL
			}

			if parent.paths != nil {
				result.paths, result.pathsDir = parent.paths, parent.pathsDir
			}
		}
	}

	if parsed.CompilerOptions.BaseURL != nil {
		result.baseURL = path.Join(dir, *parsed.CompilerOptions.BaseURL)
	}

	if parsed.CompilerOptions.Paths != nil {
		result.paths, result.pathsDir = parsed.CompilerOptions.Paths, dir
	}

	for _, reference := range parsed.References {
		target := path.Join(dir, reference.Path)
		if !strings.HasSuffix(target, ".json") {
			target = path.Join(target, "tsconfig.json")
		}

		result.references = append(result.references, target)
	}

	return result, true
}

// extendedConfig finds the file an extends names: a path from the config's
// directory, or a package in a node_modules above it.
func extendedConfig(root, dir, name string) string {
	withJSON := func(candidate string) string {
		if path.Ext(candidate) != ".json" {
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil && info.IsDir() {
				return path.Join(candidate, "tsconfig.json")
			}

			return candidate + ".json"
		}

		return candidate
	}

	if strings.HasPrefix(name, "./") || strings.HasPrefix(name, "../") {
		return withJSON(path.Join(dir, name))
	}

	for at := dir; ; at = path.Dir(at) {
		candidate := withJSON(path.Join(at, "node_modules", name))
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil {
			return candidate
		}

		if at == "." || at == "" {
			return candidate
		}
	}
}

// rules turns the settings' paths into rules, longest prefix first.
func (this tsSettings) rules() (result []pathRule) {
	base := this.pathsDir
	if this.baseURL != "" {
		base = this.baseURL
	}

	for pattern, targets := range this.paths {
		rule := pathRule{prefix: pattern}
		if before, after, found := strings.Cut(pattern, "*"); found {
			rule = pathRule{prefix: before, suffix: after, wildcard: true}
		}

		for _, target := range targets {
			rule.targets = append(rule.targets, path.Join(base, target))
		}

		result = append(result, rule)
	}

	sortRules(result)
	return result
}

// readBundlerAliases reads the resolve.alias entries of a directory's Vite
// or webpack config, as an object, { "@": path.resolve(__dirname, "src") },
// or as Vite's list, [{ find: "@", replacement: "/src" }]. A target is
// every string in its value joined from the config's directory, a leading
// slash meaning that directory, as Vite has it. A regular expression find
// is left out.
func readBundlerAliases(root, dir string) (result []pathRule) {
	for _, name := range bundlerConfigs {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name))
		if err != nil {
			continue
		}

		tokens := lex(src, false)
		for index := 0; index+2 < len(tokens); index++ {
			if (tokens[index].is("alias") || quoted(tokens[index]) == "alias") && tokens[index+1].is(":") {
				result = append(result, aliasRules(dir, tokens, index+2)...)
			}
		}
	}

	sortRules(result)
	return result
}

// aliasRules reads the alias object or list starting at index.
func aliasRules(dir string, tokens []token, index int) (result []pathRule) {
	add := func(find string, value []token) {
		target, joined := dir, false
		for _, each := range value {
			if text, ok := unquote(each); ok {
				target, joined = path.Join(target, strings.TrimPrefix(text, "/")), true
			}
		}

		switch exact, ok := strings.CutSuffix(find, "$"); {
		case find == "" || !joined:
		case ok:
			result = append(result, pathRule{prefix: exact, targets: []string{target}})
		default:
			result = append(result, pathRule{prefix: find, targets: []string{target}}, pathRule{prefix: find + "/", wildcard: true, targets: []string{target + "/*"}})
		}
	}

	for _, entry := range split(tokens[index:]) {
		switch {
		case tokens[index].is("{") && len(entry) >= 3 && entry[1].is(":"):
			key := quoted(entry[0])
			if entry[0].kind == tokenIdent {
				key = entry[0].text
			}

			add(key, entry[2:])
		case tokens[index].is("[") && len(entry) > 0 && entry[0].is("{"):
			var find string
			var value []token
			for _, field := range split(entry) {
				switch {
				case len(field) < 3 || !field[1].is(":"):
				case field[0].is("find"):
					find = quoted(field[2])
				case field[0].is("replacement"):
					value = field[2:]
				}
			}

			add(find, value)
		}
	}

	return result
}

// split divides the object, list or call opening tokens into its
// top-level entries, each a run of tokens between commas.
func split(tokens []token) (result [][]token) {
	if len(tokens) == 0 || !tokens[0].is("{") && !tokens[0].is("[") && !tokens[0].is("(") {
		return nil
	}

	depth, start := 0, 1
	for at, each := range tokens {
		switch {
		case each.is("{") || each.is("[") || each.is("("):
			depth++
		case each.is("}") || each.is("]") || each.is(")"):
			depth--
			if depth == 0 {
				if at > start {
					result = append(result, tokens[start:at])
				}

				return result
			}
		case depth == 1 && each.is(","):
			result = append(result, tokens[start:at])
			start = at + 1
		}
	}

	return result
}

// quoted gives a string token's text, or empty for any other token.
func quoted(each token) string {
	text, _ := unquote(each)
	return text
}

// sortRules puts the longest prefix first, so the most specific rule wins.
func sortRules(rules []pathRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if len(a.prefix) != len(b.prefix) {
			return len(a.prefix) > len(b.prefix)
		}

		return a.prefix+a.suffix < b.prefix+b.suffix
	})
}

// readManifest reads a directory's package.json, or nil when it has none.
func readManifest(root, dir string) *manifest {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "package.json"))
	if err != nil {
		return nil
	}

	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil {
		return &manifest{dir: dir}
	}

	result := &manifest{dir: dir}
	result.name, _ = parsed["name"].(string)
	result.private, _ = parsed["private"].(bool)
	if source, ok := parsed["source"].(string); ok {
		result.entries = append(result.entries, source)
	}

	result.entries = append(result.entries, exportTargets(parsed["exports"])...)
	if imports, ok := parsed["imports"].(map[string]any); ok {
		for pattern, value := range imports {
			rule := pathRule{prefix: pattern}
			if before, after, found := strings.Cut(pattern, "*"); found {
				rule = pathRule{prefix: before, suffix: after, wildcard: true}
			}

			for _, target := range exportTargets(value) {
				rule.targets = append(rule.targets, path.Join(dir, target))
			}

			result.imports = append(result.imports, rule)
		}

		sortRules(result.imports)
	}

	for _, field := range []string{"module", "main"} {
		if value, ok := parsed[field].(string); ok {
			result.entries = append(result.entries, value)
		}
	}

	return result
}

// stripJSONComments removes the comments and trailing commas that
// tsconfig.json allows and JSON does not.
func stripJSONComments(data []byte) []byte {
	var result []byte
	inString := false
	for index := 0; index < len(data); index++ {
		c := data[index]
		switch {
		case inString:
			result = append(result, c)
			if c == '\\' && index+1 < len(data) {
				index++
				result = append(result, data[index])
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			result = append(result, c)
		case c == '/' && index+1 < len(data) && data[index+1] == '/':
			for index < len(data) && data[index] != '\n' {
				index++
			}

			result = append(result, '\n')
		case c == '/' && index+1 < len(data) && data[index+1] == '*':
			end := strings.Index(string(data[index+2:]), "*/")
			if end < 0 {
				return result
			}

			index += 2 + end + 1
		default:
			result = append(result, c)
		}
	}

	return trailingComma.ReplaceAll(result, []byte("$1"))
}
