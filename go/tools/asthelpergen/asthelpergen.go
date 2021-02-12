/*
Copyright 2021 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/types"
	"io/ioutil"
	"log"
	"path"
	"strings"

	"github.com/dave/jennifer/jen"
	"golang.org/x/tools/go/packages"
)

const licenseFileHeader = `Copyright 2021 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.`

type astHelperGen struct {
	DebugTypes bool
	mod        *packages.Module
	sizes      types.Sizes
	iface      *types.Named
}

type rewriterFile struct {
	pkg            string
	cases          []jen.Code
	replaceMethods []jen.Code
}

func newGenerator(mod *packages.Module, sizes types.Sizes, named *types.Named) *astHelperGen {
	return &astHelperGen{
		DebugTypes: true,
		mod:        mod,
		sizes:      sizes,
		iface:      named,
	}
}

func findImplementations(scope *types.Scope, iff *types.Interface, impl func(types.Type)) {
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		baseType := obj.Type()
		if types.Implements(baseType, iff) || types.Implements(types.NewPointer(baseType), iff) {
			impl(baseType)
		}
	}
}

func (gen *astHelperGen) rewriterStructCase(name string, stroct *types.Struct) (jen.Code, error) {
	fmt.Println(stroct)
	return jen.Case(
		jen.Op("*").Id(name),
	), nil
}

func (gen *astHelperGen) rewriterReplaceMethods(name string, stroct *types.Struct) ([]jen.Code, error) {
	fmt.Println(stroct)
	fmt.Println(name)
	return nil, nil
}

func (gen *astHelperGen) doIt() (map[string]*jen.File, error) {
	pkg := gen.iface.Obj().Pkg()
	rewriter := &rewriterFile{}

	iface, ok := gen.iface.Underlying().(*types.Interface)
	if !ok {
		return nil, fmt.Errorf("expected interface, but got %T", gen.iface)
	}

	var outerErr error

	findImplementations(pkg.Scope(), iface, func(t types.Type) {
		switch n := t.Underlying().(type) {
		case *types.Struct:
			switchCase, err := gen.rewriterStructCase(t.String(), n)
			if err != nil {
				outerErr = err
				return
			}
			rewriter.cases = append(rewriter.cases, switchCase)

			replaceMethods, err := gen.rewriterReplaceMethods(t.String(), n)
			if err != nil {
				outerErr = err
				return
			}
			rewriter.replaceMethods = append(rewriter.cases, replaceMethods...)
		case *types.Interface:

		default:
			fmt.Printf("unknown %T\n", t)
		}
	})

	if outerErr != nil {
		return nil, outerErr
	}

	result := map[string]*jen.File{}
	fullPath := path.Join(gen.mod.Dir, strings.TrimPrefix(pkg.Path(), gen.mod.Path), "rewriter.go")
	result[fullPath] = gen.rewriterFile(pkg.Name(), rewriter)

	return result, nil
}

func (gen *astHelperGen) rewriterFile(pkgName string, file *rewriterFile) *jen.File {
	out := jen.NewFile(pkgName)
	out.HeaderComment(licenseFileHeader)
	out.HeaderComment("Code generated by ASTHelperGen. DO NOT EDIT.")

	apply := gen.rewriterApplyFunc()

	out.Add(apply)

	return out
}

func (gen *astHelperGen) rewriterApplyFunc() *jen.Statement {
	apply := jen.Func().Params(
		jen.Id("a").Op("*").Id("application"),
	).Id("apply").Params(
		jen.Id("parent"),
		jen.Id("node").Id(gen.iface.Obj().Name()),
		jen.Id("replacer").Id("replacerFunc"),
	).Block(
		jen.If(
			jen.Id("node").Op("==").Nil().Op("||").
				Id("isNilValue").Call(jen.Id("node"))).Block(
			jen.Return(),
		),
		jen.Id("saved").Op(":=").Id("a").Dot("cursor"),
		jen.Id("a").Dot("cursor").Dot("replacer").Op("=").Id("replacer"),
		jen.Id("a").Dot("cursor").Dot("node").Op("=").Id("node"),
		jen.Id("a").Dot("cursor").Dot("parent").Op("=").Id("parent"),
		jen.If(
			jen.Id("a").Dot("pre").Op("!=").Nil().Op("&&").
				Op("!").Id("a").Dot("pre").Call(jen.Op("&").Id("a").Dot("cursor"))).Block(
			jen.Id("a").Dot("cursor").Op("=").Id("saved"),
			jen.Return(),
		),
		jen.If(
			jen.Id("a").Dot("post").Op("!=").Nil().Op("&&").
				Op("!").Id("a").Dot("post").Call(jen.Op("&").Id("a").Dot("cursor"))).Block(
			jen.Id("panic").Call(jen.Id("abort")),
		),
	)
	return apply
}

type typePaths []string

func (t *typePaths) String() string {
	return fmt.Sprintf("%v", *t)
}

func (t *typePaths) Set(path string) error {
	*t = append(*t, path)
	return nil
}

func main() {
	var patterns typePaths
	var generate string
	var verify bool

	flag.Var(&patterns, "in", "Go packages to load the generator")
	flag.StringVar(&generate, "iface", "", "Root interface generate rewriter for")
	flag.BoolVar(&verify, "verify", false, "ensure that the generated files are correct")
	flag.Parse()

	result, err := GenerateASTHelpers(patterns, generate)
	if err != nil {
		log.Fatal(err)
	}

	if verify {
		for _, err := range VerifyFilesOnDisk(result) {
			log.Fatal(err)
		}
		log.Printf("%d files OK", len(result))
	} else {
		for fullPath, file := range result {
			if err := file.Save(fullPath); err != nil {
				log.Fatalf("failed to save file to '%s': %v", fullPath, err)
			}
			log.Printf("saved '%s'", fullPath)
		}
	}
}

// VerifyFilesOnDisk compares the generated results from the codegen against the files that
// currently exist on disk and returns any mismatches
func VerifyFilesOnDisk(result map[string]*jen.File) (errors []error) {
	for fullPath, file := range result {
		existing, err := ioutil.ReadFile(fullPath)
		if err != nil {
			errors = append(errors, fmt.Errorf("missing file on disk: %s (%w)", fullPath, err))
			continue
		}

		var buf bytes.Buffer
		if err := file.Render(&buf); err != nil {
			errors = append(errors, fmt.Errorf("render error for '%s': %w", fullPath, err))
			continue
		}

		if !bytes.Equal(existing, buf.Bytes()) {
			errors = append(errors, fmt.Errorf("'%s' has changed", fullPath))
			continue
		}
	}
	return errors
}

// GenerateASTHelpers generates the auxiliary code that implements CachedSize helper methods
// for all the types listed in typePatterns
func GenerateASTHelpers(packagePatterns []string, rootIface string) (map[string]*jen.File, error) {
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports | packages.NeedModule,
		Logf: log.Printf,
	}, packagePatterns...)

	if err != nil {
		return nil, err
	}

	scopes := make(map[string]*types.Scope)
	for _, pkg := range loaded {
		scopes[pkg.PkgPath] = pkg.Types.Scope()
	}

	pos := strings.LastIndexByte(rootIface, '.')
	if pos < 0 {
		return nil, fmt.Errorf("unexpected input type: %s", rootIface)
	}

	pkgname := rootIface[:pos]
	typename := rootIface[pos+1:]

	scope := scopes[pkgname]
	if scope == nil {
		return nil, fmt.Errorf("no scope found for type '%s'", rootIface)
	}

	tt := scope.Lookup(typename)
	if tt == nil {
		return nil, fmt.Errorf("no type called '%s' found in '%s'", typename, pkgname)
	}

	generator := newGenerator(loaded[0].Module, loaded[0].TypesSizes, tt.Type().(*types.Named))
	it, err := generator.doIt()
	if err != nil {
		return nil, err
	}

	return it, nil
}
