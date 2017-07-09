package main

// Usage: wrap -service=MyService

import (
	"fmt"
	"github.com/dforsyth/jot"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"gopkg.in/alecthomas/kingpin.v2"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"time"
)

type Context struct {
	service string

	suffix string
	outPkg string

	wd  string
	pkg string

	info *types.Info

	serviceTypeSpec *ast.TypeSpec

	makerFuncSpec     *jot.FunctionSpec
	wrapperStructSpec *jot.StructSpec
}

func (ctx *Context) openDestFile() (*os.File, error) {
	dstDir := path.Join(ctx.wd, ctx.outPkg)
	if err := os.MkdirAll(dstDir, 0777); err != nil {
		return nil, err
	}

	return os.Create(path.Join(dstDir, fmt.Sprintf("%s.go", ctx.outPkg)))
}

func makeContext() (*Context, error) {
	ctx := &Context{}

	kingpin.Flag("service", "Service to wrap.").Required().StringVar(&ctx.service)
	kingpin.Flag("wrapper", "Wrapper suffix.").Default("wrapper").StringVar(&ctx.suffix)
	kingpin.Flag("outpkg", "Destination package name.").StringVar(&ctx.outPkg)
	kingpin.Parse()

	if ctx.outPkg == "" {
		ctx.outPkg = fmt.Sprintf("%s%s", strings.ToLower(ctx.service), ctx.suffix)
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ctx.wd = wd

	// Assume where we are in relation to gopath so we can import the package into
	// the generated wrapper.
	ctx.pkg = strings.Replace(ctx.wd, path.Join(os.Getenv("GOPATH"), "src")+"/", "", -1)

	return ctx, nil
}

// Run in the directory of the package with the service to be wrapped.
func main() {
	ctx, err := makeContext()
	if err != nil {
		log.Fatal(err)
	}

	if err := ctx.process(); err != nil {
		log.Fatal(err)
	}

	ctx.generateWrapperStruct()
	ctx.generateWrapperMaker()

	if err := ctx.generate(); err != nil {
		log.Fatal(err)
	}
}

// Prepare the wrapper struct. Add it to the file and ref it in the context.
func (ctx *Context) generateWrapperStruct() {
	obj := ctx.info.Defs[ctx.serviceTypeSpec.Name]
	ctx.wrapperStructSpec = jot.Struct(fmt.Sprintf("%sWrapper", ctx.service)).
		AddField(jot.Field("service", jot.Ptr(jot.TypeTypesObject(obj))))

	if st, ok := ctx.serviceTypeSpec.Type.(*ast.StructType); ok {
		for _, field := range st.Fields.List {
			if ft, ok := field.Type.(*ast.FuncType); ok {
				for _, name := range field.Names {
					ctx.generateWrapperMethod(name.Name, ft)
				}
			}

		}
	}

	ctx.wrapperStructSpec.AddMethodRecv("r", true, jot.Function(fmt.Sprintf("Get%s", ctx.service)).
		AddReturnType(jot.Ptr(jot.TypeTypesObject(obj))).
		AddCode("return r.service"))
}

// Turn an ast.Expr into a jot.TypeSpec.
func (ctx *Context) MakeTypeSpec(expr ast.Expr) jot.TypeSpec {
	switch texpr := expr.(type) {
	case (*ast.Ident):
		return jot.TypeTypesObject(ctx.info.Uses[texpr])
	case (*ast.StarExpr):
		return jot.Ptr(ctx.MakeTypeSpec(texpr.X))
	case (*ast.SelectorExpr):
		return jot.TypeASTSelector(texpr)
	case (*ast.ArrayType):
		return jot.Array(ctx.MakeTypeSpec(texpr.Elt))
	case (*ast.MapType):
		return jot.Map(ctx.MakeTypeSpec(texpr.Key), ctx.MakeTypeSpec(texpr.Value))
	}
	log.Printf("Unsupported expr: %t\n", expr)
	return nil
}

// Generate a method that wraps a func field with a given name and ast representation..
func (ctx *Context) generateWrapperMethod(name string, fn *ast.FuncType) {
	fnSpec := jot.Function(name)

	code := "return r.service.{0}("
	for pos, param := range fn.Params.List {
		if pos > 0 {
			code += ", "
		}
		pname := fmt.Sprintf("pos%d", pos)
		fnSpec.AddParameter(pname, ctx.MakeTypeSpec(param.Type))
		code += pname
	}
	code += ")"

	for _, result := range fn.Results.List {
		fnSpec.AddReturnType(ctx.MakeTypeSpec(result.Type))
	}

	fnSpec.AddCodeFmt(code, name)

	ctx.wrapperStructSpec.AddMethodRecv("r", true, fnSpec)
}

// Generate a maker for a generated wrapper.
func (ctx *Context) generateWrapperMaker() {
	obj := ctx.info.Defs[ctx.serviceTypeSpec.Name]
	code := `return &{0}{service}`

	ctx.makerFuncSpec = jot.Function(fmt.Sprintf("Wrap%s", ctx.service)).
		AddParameter("service", jot.Ptr(jot.TypeTypesObject(obj))).
		AddCodeFmt(code, fmt.Sprintf("%sWrapper", ctx.service)).
		AddReturnType(jot.Ptr(ctx.wrapperStructSpec.TypeSpec()))
}

// Process the source package.
func (ctx *Context) process() error {
	fs := token.NewFileSet()
	pkgs, err := parser.ParseDir(fs, ctx.wd, nil, 0)
	if err != nil {
		return err
	}

	files := make([]*ast.File, 0)
	if len(pkgs) != 1 {
		return fmt.Errorf("%s must contain exactly one package.", ctx.wd)
	}

	var pkgName string
	for name, pkg := range pkgs {
		for _, file := range pkg.Files {
			files = append(files, file)
		}
		pkgName = name
	}

	conf := types.Config{Importer: importer.Default()}
	ctx.info = &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}

	if _, err := conf.Check(pkgName, fs, files, ctx.info); err != nil {
		return err
	}

	for _, file := range files {
		for _, d := range file.Decls {
			switch decl := d.(type) {
			case *ast.GenDecl:
				if len(decl.Specs) != 1 {
					continue
				}
				if ts, ok := decl.Specs[0].(*ast.TypeSpec); ok {
					if ts.Name.Name == ctx.service {
						ctx.serviceTypeSpec = ts
						return nil
					}
				}
			}
		}
	}

	return fmt.Errorf("Service %s not found.", ctx.service)
}

const GENERATED_BY = "// Generated by reflectclient/wrap at %s\n"

// Generate the source file.
func (ctx *Context) generate() error {
	f, err := ctx.openDestFile()
	if err != nil {
		return err
	}

	if _, err := io.WriteString(f, fmt.Sprintf(GENERATED_BY, time.Now().UTC())); err != nil {
		return err
	}

	return jot.File(fmt.Sprintf("%s%s", strings.ToLower(ctx.service), ctx.suffix)).
		AddImport(jot.Import(ctx.pkg)).
		AddStruct(ctx.wrapperStructSpec).
		AddFunction(ctx.makerFuncSpec).
		Generate(f)
}
