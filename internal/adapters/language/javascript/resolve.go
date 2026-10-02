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
	sourceExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
	trailingComma    = regexp.MustCompile(`,(\s*[}\]])`)
	typeScriptTwins  = map[string][]string{".js": {".ts", ".tsx"}, ".jsx": {".tsx"}, ".mjs": {".mts"}, ".cjs": {".cts"}}
)

// compilerConfig is the module resolution a tsconfig.json or jsconfig.json
// sets up for the files beneath it.
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

// readCompilerConfig reads the baseUrl and paths of a directory's
// tsconfig.json or jsconfig.json, or nil when it has neither or they set no
// module resolution. A config's extends is not followed.
func readCompilerConfig(root, dir string) *compilerConfig {
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name))
		if err != nil {
			continue
		}

		var parsed struct {
			CompilerOptions struct {
				BaseURL string              `json:"baseUrl"`
				Paths   map[string][]string `json:"paths"`
			} `json:"compilerOptions"`
		}

		if json.Unmarshal(stripJSONComments(data), &parsed) != nil {
			return nil
		}

		options := parsed.CompilerOptions
		if options.BaseURL == "" && len(options.Paths) == 0 {
			return nil
		}

		result := &compilerConfig{}
		base := dir
		if options.BaseURL != "" {
			result.baseURL = path.Join(dir, options.BaseURL)
			base = result.baseURL
		}

		for pattern, targets := range options.Paths {
			rule := pathRule{prefix: pattern}
			if before, after, found := strings.Cut(pattern, "*"); found {
				rule = pathRule{prefix: before, suffix: after, wildcard: true}
			}

			for _, target := range targets {
				rule.targets = append(rule.targets, path.Join(base, target))
			}

			result.paths = append(result.paths, rule)
		}

		sort.Slice(result.paths, func(i, j int) bool {
			a, b := result.paths[i], result.paths[j]
			if len(a.prefix) != len(b.prefix) {
				return len(a.prefix) > len(b.prefix)
			}

			return a.prefix+a.suffix < b.prefix+b.suffix
		})
		return result
	}

	return nil
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
