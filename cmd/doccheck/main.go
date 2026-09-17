// Command doccheck verifies repository documentation contracts without
// modifying the working tree.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const defaultAtlasPath = "docs/atlas.md"

var (
	atlasEntryPattern   = regexp.MustCompile("^- `([^`]+)` — (.+)$")
	atlasVersionPattern = regexp.MustCompile(`^Version suivie : \*\*v([0-9]+\.[0-9]+\.[0-9]+)\*\*\.$`)
)

type finding struct {
	path    string
	line    int
	message string
}

func (item finding) String() string {
	if item.line > 0 {
		return fmt.Sprintf("%s:%d: %s", item.path, item.line, item.message)
	}
	return fmt.Sprintf("%s: %s", item.path, item.message)
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "doccheck: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("doccheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "repository root")
	scope := flags.String("scope", "all", "checks to run: all, atlas, or go")
	atlasPath := flags.String("atlas", defaultAtlasPath, "atlas path relative to the repository root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *scope != "all" && *scope != "atlas" && *scope != "go" {
		return fmt.Errorf("invalid scope %q (want all, atlas, or go)", *scope)
	}

	files, err := repositoryFiles(ctx, *root)
	if err != nil {
		return err
	}
	var findings []finding
	if *scope == "all" || *scope == "atlas" {
		atlasFindings, err := checkAtlas(os.DirFS(*root), files, *atlasPath)
		if err != nil {
			return err
		}
		findings = append(findings, atlasFindings...)
	}
	if *scope == "all" || *scope == "go" {
		goFindings, err := checkGo(os.DirFS(*root), files)
		if err != nil {
			return err
		}
		findings = append(findings, goFindings...)
	}
	sortFindings(findings)
	if len(findings) != 0 {
		for _, item := range findings {
			fmt.Fprintln(output, item)
		}
		return fmt.Errorf("documentation checks failed with %d finding(s)", len(findings))
	}
	fmt.Fprintf(output, "doccheck: %s OK\n", *scope)
	return nil
}

func repositoryFiles(ctx context.Context, root string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	data, err := command.Output()
	if err != nil {
		return nil, errors.New("list repository files with git")
	}
	parts := bytes.Split(data, []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(part)))
	}
	sort.Strings(files)
	return files, nil
}

func checkAtlas(source fs.FS, files []string, atlasPath string) ([]finding, error) {
	content, err := fs.ReadFile(source, atlasPath)
	if err != nil {
		return nil, fmt.Errorf("read atlas: %w", err)
	}
	version, err := sourceVersion(source, "internal/version/version.go")
	if err != nil {
		return nil, err
	}

	entries := make(map[string]int)
	var findings []finding
	var atlasVersion string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if matches := atlasVersionPattern.FindStringSubmatch(line); matches != nil {
			atlasVersion = matches[1]
		}
		if strings.HasPrefix(line, "- `") {
			matches := atlasEntryPattern.FindStringSubmatch(line)
			if matches == nil {
				findings = append(findings, finding{path: atlasPath, line: lineNumber, message: "invalid atlas entry; want - `path` — description"})
				continue
			}
			path := filepath.ToSlash(matches[1])
			if strings.TrimSpace(matches[2]) == "" {
				findings = append(findings, finding{path: atlasPath, line: lineNumber, message: "atlas entry needs a non-empty description"})
				continue
			}
			if path != matches[1] || path == "." || strings.HasPrefix(path, "../") || filepath.Clean(path) != path {
				findings = append(findings, finding{path: atlasPath, line: lineNumber, message: "atlas entry path must be a canonical repository-relative path"})
				continue
			}
			if previous, exists := entries[path]; exists {
				findings = append(findings, finding{path: atlasPath, line: lineNumber, message: fmt.Sprintf("duplicate entry for %s (first declared at line %d)", path, previous)})
				continue
			}
			entries[path] = lineNumber
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan atlas: %w", err)
	}
	if atlasVersion == "" {
		findings = append(findings, finding{path: atlasPath, message: "missing tracked version banner"})
	} else if atlasVersion != version {
		findings = append(findings, finding{path: atlasPath, message: fmt.Sprintf("tracks v%s, want v%s", atlasVersion, version)})
	}

	repositorySet := make(map[string]struct{}, len(files))
	for _, path := range files {
		repositorySet[path] = struct{}{}
		if _, exists := entries[path]; !exists {
			findings = append(findings, finding{path: atlasPath, message: "missing entry for " + path})
		}
	}
	for path, line := range entries {
		if _, exists := repositorySet[path]; !exists {
			findings = append(findings, finding{path: atlasPath, line: line, message: "stale entry for " + path})
		}
	}
	return findings, nil
}

func checkGo(source fs.FS, files []string) ([]finding, error) {
	type packageState struct {
		name       string
		documented bool
		firstPath  string
	}

	packages := make(map[string]*packageState)
	var findings []finding
	for _, path := range files {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, err := fs.ReadFile(source, path)
		if err != nil {
			return nil, fmt.Errorf("read Go source %s: %w", path, err)
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, content, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse Go source %s: %w", path, err)
		}
		if ast.IsGenerated(file) {
			continue
		}

		directory := filepath.ToSlash(filepath.Dir(path))
		state, exists := packages[directory]
		if !exists {
			state = &packageState{name: file.Name.Name, firstPath: path}
			packages[directory] = state
		}
		if validPackageComment(file.Name.Name, commentText(file.Doc)) {
			state.documented = true
		}

		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Name.IsExported() && receiverIsDocumentedAPI(declaration.Recv) && !commentStartsWith(declaration.Doc, declaration.Name.Name) {
					findings = append(findings, positionFinding(fileSet, path, declaration.Pos(), "exported function or method "+declaration.Name.Name+" needs a GoDoc comment starting with its name"))
				}
			case *ast.GenDecl:
				findings = append(findings, checkGeneralDeclaration(fileSet, path, declaration)...)
			}
		}
	}
	for _, state := range packages {
		if !state.documented {
			findings = append(findings, finding{path: state.firstPath, message: fmt.Sprintf("package %s needs a package or command comment", state.name)})
		}
	}
	return findings, nil
}

func receiverIsDocumentedAPI(receiver *ast.FieldList) bool {
	if receiver == nil || len(receiver.List) == 0 {
		return true
	}
	typeExpression := receiver.List[0].Type
	if pointer, ok := typeExpression.(*ast.StarExpr); ok {
		typeExpression = pointer.X
	}
	identifier, ok := typeExpression.(*ast.Ident)
	return ok && identifier.IsExported()
}

func checkGeneralDeclaration(fileSet *token.FileSet, path string, declaration *ast.GenDecl) []finding {
	if declaration.Tok != token.TYPE && declaration.Tok != token.CONST && declaration.Tok != token.VAR {
		return nil
	}
	var findings []finding
	for _, rawSpec := range declaration.Specs {
		switch spec := rawSpec.(type) {
		case *ast.TypeSpec:
			if spec.Name.IsExported() {
				doc := spec.Doc
				if doc == nil {
					doc = declaration.Doc
				}
				if !commentStartsWith(doc, spec.Name.Name) {
					findings = append(findings, positionFinding(fileSet, path, spec.Pos(), "exported type "+spec.Name.Name+" needs a GoDoc comment starting with its name"))
				}
			}
			if structure, ok := spec.Type.(*ast.StructType); ok {
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						if name.IsExported() && !commentStartsWithEither(field.Doc, field.Comment, name.Name) {
							findings = append(findings, positionFinding(fileSet, path, name.Pos(), "exported field "+name.Name+" needs a comment starting with its name"))
						}
					}
				}
			}
		case *ast.ValueSpec:
			for _, name := range spec.Names {
				if !name.IsExported() {
					continue
				}
				doc := spec.Doc
				if doc == nil {
					doc = declaration.Doc
				}
				if !commentStartsWithEither(doc, spec.Comment, name.Name) {
					findings = append(findings, positionFinding(fileSet, path, name.Pos(), "exported value "+name.Name+" needs a GoDoc comment starting with its name"))
				}
			}
		}
	}
	return findings
}

func validPackageComment(packageName, text string) bool {
	if packageName == "main" {
		return strings.HasPrefix(text, "Command ") || strings.HasPrefix(text, "Package main ")
	}
	return strings.HasPrefix(text, "Package "+packageName+" ")
}

func commentStartsWith(group *ast.CommentGroup, name string) bool {
	return group != nil && startsWithName(commentText(group), name)
}

func commentStartsWithEither(first, second *ast.CommentGroup, name string) bool {
	return commentStartsWith(first, name) || commentStartsWith(second, name)
}

func commentText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	return strings.TrimSpace(group.Text())
}

func startsWithName(text, name string) bool {
	return text == name || strings.HasPrefix(text, name+" ") || strings.HasPrefix(text, name+".") || strings.HasPrefix(text, name+",")
}

func positionFinding(fileSet *token.FileSet, path string, position token.Pos, message string) finding {
	return finding{path: path, line: fileSet.Position(position).Line, message: message}
}

func sourceVersion(source fs.FS, path string) (string, error) {
	content, err := fs.ReadFile(source, path)
	if err != nil {
		return "", fmt.Errorf("read version source: %w", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, content, 0)
	if err != nil {
		return "", fmt.Errorf("parse version source: %w", err)
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, rawSpec := range general.Specs {
			spec, ok := rawSpec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range spec.Names {
				if name.Name != "Version" || index >= len(spec.Values) {
					continue
				}
				literal, ok := spec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return "", errors.New("version source is not a string literal")
				}
				return strings.Trim(literal.Value, `"`), nil
			}
		}
	}
	return "", errors.New("Version constant not found")
}

func sortFindings(findings []finding) {
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].path != findings[right].path {
			return findings[left].path < findings[right].path
		}
		if findings[left].line != findings[right].line {
			return findings[left].line < findings[right].line
		}
		return findings[left].message < findings[right].message
	})
}
