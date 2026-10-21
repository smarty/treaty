package csharp

import (
	"encoding/xml"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const propsName = "Directory.Build.props"

var (
	testPackages   = []string{"Microsoft.NET.Test.Sdk", "xunit", "xunit.v3", "NUnit", "MSTest", "MSTest.TestFramework", "TUnit"}
	testReferences = []string{"nunit.framework", "xunit.core", "Microsoft.VisualStudio.QualityTools.UnitTestFramework"}
	testSuffixes   = []string{".Tests", ".UnitTests", ".IntegrationTests", ".FunctionalTests"}
	appSdks        = []string{"Microsoft.NET.Sdk.Web", "Microsoft.NET.Sdk.Worker", "Microsoft.NET.Sdk.BlazorWebAssembly"}
)

// project is one .csproj, or the implicit project of the files that have
// none.
type project struct {
	path       string
	dir        string
	name       string
	test       bool
	executable bool
	references []string
	usings     []usingDirective
}

// skip is a file the extractor could not read, and why.
type skip struct {
	path   string
	reason string
}

// projectSet is every project in a tree.
type projectSet struct {
	byPath   map[string]*project
	byDir    map[string]*project
	implicit *project
	skipped  []skip
	used     map[string]bool
}

// msbuild is the part of a project or props file that Treaty reads.
type msbuild struct {
	Sdk        string       `xml:"Sdk,attr"`
	SdkNodes   []sdkNode    `xml:"Sdk"`
	Properties []propGroup  `xml:"PropertyGroup"`
	Items      []itemGroup  `xml:"ItemGroup"`
	Imports    []importNode `xml:"Import"`
}

type sdkNode struct {
	Name string `xml:"Name,attr"`
}

type propGroup struct {
	Condition string     `xml:"Condition,attr"`
	Values    []property `xml:",any"`
}

type property struct {
	XMLName   xml.Name
	Condition string `xml:"Condition,attr"`
	Value     string `xml:",chardata"`
}

type itemGroup struct {
	Condition string `xml:"Condition,attr"`
	Packages  []item `xml:"PackageReference"`
	Legacy    []item `xml:"Reference"`
	Projects  []item `xml:"ProjectReference"`
	Usings    []item `xml:"Using"`
}

type item struct {
	Include   string `xml:"Include,attr"`
	Condition string `xml:"Condition,attr"`
	Alias     string `xml:"Alias,attr"`
	Static    string `xml:"Static,attr"`
}

type importNode struct {
	Project   string `xml:"Project,attr"`
	Sdk       string `xml:"Sdk,attr"`
	Condition string `xml:"Condition,attr"`
}

// readProjects reads the project and props files of a tree. A file that
// does not parse as XML is skipped and recorded.
//
// Parameters:
//   - root: the repository root.
//   - paths: the .csproj and Directory.Build.props files, relative to root.
//
// Returns:
//   - result: the projects.
//   - err: a file could not be read.
func readProjects(root string, paths []string) (result *projectSet, err error) {
	result = &projectSet{byPath: map[string]*project{}, byDir: map[string]*project{}, implicit: &project{path: "", dir: "."}}
	parsed := map[string]*msbuild{}
	for _, name := range paths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}

		file := &msbuild{}
		if err := xml.Unmarshal(content, file); err != nil {
			result.skipped = append(result.skipped, skip{path: name, reason: err.Error()})
			continue
		}

		parsed[name] = file
	}

	for _, name := range paths {
		file := parsed[name]
		if file == nil || !strings.HasSuffix(name, ".csproj") {
			continue
		}

		dir := path.Dir(name)
		each := &project{path: name, dir: dir, name: strings.TrimSuffix(path.Base(name), ".csproj")}
		files := append([]*msbuild{file}, props(parsed, dir)...)
		output := ""
		for at, current := range files {
			for _, group := range current.Properties {
				if group.Condition != "" {
					continue
				}

				for _, value := range group.Values {
					if value.Condition != "" {
						continue
					}

					text := strings.TrimSpace(value.Value)
					switch value.XMLName.Local {
					case "IsTestProject":
						each.test = each.test || strings.EqualFold(text, "true")
					case "OutputType":
						if output == "" {
							output = text
						}
					case "AssemblyName":
						if at == 0 && text != "" && !strings.Contains(text, "$(") {
							each.name = text
						}
					}
				}
			}

			for _, group := range current.Items {
				if group.Condition != "" {
					continue
				}

				for _, reference := range group.Packages {
					if reference.Condition == "" && slices.ContainsFunc(testPackages, func(name string) bool { return strings.EqualFold(name, reference.Include) }) {
						each.test = true
					}
				}

				for _, reference := range group.Legacy {
					name, _, _ := strings.Cut(reference.Include, ",")
					if reference.Condition == "" && slices.ContainsFunc(testReferences, func(known string) bool { return strings.EqualFold(known, strings.TrimSpace(name)) }) {
						each.test = true
					}
				}

				if at > 0 {
					continue
				}

				for _, reference := range group.Projects {
					if reference.Condition == "" && reference.Include != "" {
						each.references = append(each.references, path.Clean(path.Join(dir, strings.ReplaceAll(reference.Include, `\`, "/"))))
					}
				}
			}

			for _, group := range current.Items {
				if group.Condition != "" {
					continue
				}

				for _, using := range group.Usings {
					if using.Condition == "" && using.Include != "" {
						each.usings = append(each.usings, usingDirective{alias: using.Alias, target: using.Include, static: strings.EqualFold(using.Static, "true"), global: true})
					}
				}
			}
		}

		sdk := sdkOf(file)
		if sdk == "MSTest.Sdk" {
			each.test = true
		}

		for _, suffix := range testSuffixes {
			if strings.HasSuffix(each.name, suffix) || strings.HasSuffix(strings.TrimSuffix(path.Base(name), ".csproj"), suffix) {
				each.test = true
			}
		}

		switch {
		case strings.EqualFold(output, "Exe") || strings.EqualFold(output, "WinExe"):
			each.executable = true
		case output == "" && slices.Contains(appSdks, sdk):
			each.executable = true
		}

		result.byPath[name] = each
		if result.byDir[dir] == nil {
			result.byDir[dir] = each
		}
	}

	result.used = map[string]bool{}
	for _, each := range result.byPath {
		if each.test {
			continue
		}

		for _, reference := range each.references {
			result.used[reference] = true
		}
	}

	return result, nil
}

// props lists the props files a project imports: the nearest
// Directory.Build.props above it, and each parent that one imports.
func props(parsed map[string]*msbuild, dir string) []*msbuild {
	var result []*msbuild
	for at := dir; ; at = path.Dir(at) {
		file := parsed[path.Join(at, propsName)]
		if file != nil {
			result = append(result, file)
			if !importsParent(file) {
				return result
			}
		}

		if at == "." || at == "/" {
			return result
		}
	}
}

// importsParent reports whether a props file imports the props file above
// it, with GetPathOfFileAbove or GetDirectoryNameOfFileAbove.
func importsParent(file *msbuild) bool {
	for _, each := range file.Imports {
		if strings.Contains(each.Project, propsName) && (strings.Contains(each.Project, "GetPathOfFileAbove") || strings.Contains(each.Project, "GetDirectoryNameOfFileAbove")) {
			return true
		}
	}

	return false
}

// sdkOf gives a project's SDK name without its version.
func sdkOf(file *msbuild) string {
	sdk := file.Sdk
	if sdk == "" && len(file.SdkNodes) > 0 {
		sdk = file.SdkNodes[0].Name
	}

	for _, each := range file.Imports {
		if sdk == "" && each.Sdk != "" {
			sdk = each.Sdk
		}
	}

	sdk, _, _ = strings.Cut(strings.TrimSpace(sdk), ";")
	sdk, _, _ = strings.Cut(sdk, "/")
	return sdk
}

// owner finds the project of a .cs file: the nearest project file above it,
// or the implicit project.
func (this *projectSet) owner(file string) *project {
	for at := path.Dir(file); ; at = path.Dir(at) {
		if found := this.byDir[at]; found != nil {
			return found
		}

		if at == "." || at == "/" {
			return this.implicit
		}
	}
}

// closure lists the projects whose types a project can use: itself and
// every project it references, directly or not.
func (this *projectSet) closure(start *project) map[*project]bool {
	result := map[*project]bool{}
	var visit func(*project)
	visit = func(each *project) {
		if each == nil || result[each] {
			return
		}

		result[each] = true
		for _, reference := range each.references {
			visit(this.byPath[reference])
		}
	}

	visit(start)
	return result
}

// entry reports whether a project's program directories are entries: it is
// executable and no project other than a test project references it.
func (this *projectSet) entry(each *project) bool {
	return each != nil && each.executable && !this.used[each.path]
}

// sorted lists the projects in path order.
func (this *projectSet) sorted() []*project {
	var result []*project
	for _, each := range this.byPath {
		result = append(result, each)
	}

	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}
