package gen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	gobuild "go/build"
	"go/doc"
	"go/format"
	goparser "go/parser"
	"go/token"
	"io"
	"log"
	"os"
	"strings"
)

func CarGen(name, dbName, grpcPath, grpcPkgName, distPath, distPkg string) {
	err := (&Generator{Model: name, DbName: dbName, PkgPath: grpcPath, PkgName: grpcPkgName, DistPath: distPath, DistPkg: distPkg,
		ServiceFields: []ServiceField{
			{Field: "*query.Query", Import: `"/orm/` + dbName + `/query"`},
			{Field: "cache *redisd.Decorator", Import: `redisd "comm/redis"`},
			{Field: "conf  *config.Config", Import: `"` + name + `/config"`},
		},
	}).Run()
	if err != nil {
		panic(err)
	}
}

type Generator struct {
	Model              string
	PkgPath            string
	PkgName            string
	DistPath           string
	DistPkg            string
	DbName             string
	hasQuery           bool
	ServiceFields      []ServiceField
	MethodImports      []string
	ServiceFiles       []ServiceFileInfo
	serMethodStructDoc []*doc.Type
}

// ServiceFileInfo 文件内容
type ServiceFileInfo struct {
	FileInfo
}

type FileInfo struct {
	Name   string
	Buffer bytes.Buffer
}

type ServiceField struct {
	Import string
	Field  string
}

func (g *Generator) Run() error {
	pkg, err := parseDir(g.PkgPath, g.PkgName)
	if err != nil {
		return err
	}
	pkgs := doc.New(pkg, g.PkgPath, doc.AllMethods)
	intfs := g.findPbInterface(pkgs)
	//优先生成服务方法文件 后续生成服务文件好不全对应初始化方法
	g.generateMethodFile(intfs)
	//生成服务文件
	g.generateServiceFile(intfs)
	return err
}

// 寻找grpc生成文件中的接口
func (g *Generator) findPbInterface(pkg *doc.Package) []*ast.TypeSpec {
	var interfaceTypes []*ast.TypeSpec
	for _, t := range pkg.Types {
		for _, s := range t.Decl.Specs {
			spec := s.(*ast.TypeSpec)
			_, ok := spec.Type.(*ast.InterfaceType)
			if ok {
				interfaceTypes = append(interfaceTypes, spec)
			}
		}
	}
	return interfaceTypes
}

// 寻找grpc生成文件中的接口
func (g *Generator) findStruct(pkg *doc.Package) []*doc.Type {
	var structTypes []*doc.Type
	for _, t := range pkg.Types {
		for _, s := range t.Decl.Specs {
			spec := s.(*ast.TypeSpec)
			_, ok := spec.Type.(*ast.StructType)
			if ok {
				structTypes = append(structTypes, t)
			}
		}
	}
	return structTypes
}

// 刷新服务方法文件文档
func (g *Generator) refreshServiceMethodStructDoc() {
	//初始化 现有方法文件代码文档
	pkg, err := parseDir(g.DistPath, g.DistPkg)
	if err != nil {
		panic(err)
	}
	methodPkgs := doc.New(pkg, g.DistPath, doc.AllDecls)
	g.serMethodStructDoc = g.findStruct(methodPkgs)
}

// 组装服务文件
func (g *Generator) generateServiceFile(intfs []*ast.TypeSpec) {
	g.refreshServiceMethodStructDoc()
	//生成服务文件及方法初始化
	for _, t := range intfs {
		var info ServiceFileInfo
		info.Name = t.Name.Name
		info.Buffer.WriteString("// Code generated by car-gen. DO NOT EDIT.\npackage " + g.DistPkg + "\n\n")
		//部分头部定义需要方法读取完毕后才可生成，这里优先执行
		method := g.generateServiceMethod(t)
		header := g.generateServiceHeader()
		define := g.generateServiceDefine(t)
		info.Buffer.WriteString(header)
		info.Buffer.WriteString(define)
		info.Buffer.WriteString(method)
		err := WriteByteFile(g.DistPath+ToSnakeCase(info.Name)+".gen.go", info.Buffer.Bytes())
		if err != nil {
			panic(err)
		}
	}
}

// 生成服务的头部
func (g *Generator) generateServiceHeader() string {
	var tx string
	if g.hasQuery {
		tx = "\t\"orm/" + g.DbName + "/query\"\n"
	}
	structStr := fmt.Sprintf("import (\n\t\"context\"\n\t\"%s\"\n%s)\n\n", g.Model+"/rpc/kitex_gen/pb", tx)
	return structStr
}

// 组装服务的定义
func (g *Generator) generateServiceDefine(s *ast.TypeSpec) string {
	structStr := fmt.Sprintf("var %s %s.%s = &%s{}\n\n", "I"+s.Name.Name, g.PkgName, s.Name.Name, s.Name.Name)
	return structStr
}

// 组装服务方法
func (g *Generator) generateServiceMethod(s *ast.TypeSpec) string {
	t := s.Type.(*ast.InterfaceType)
	var structStr string
	for _, m := range t.Methods.List {
		f := m.Type.(*ast.FuncType)
		p := f.Params.List[1].Type.(*ast.StarExpr).X.(*ast.Ident)
		r := f.Results.List[0].Type.(*ast.StarExpr).X.(*ast.Ident)
		fieldStr, txStr := g.generateServiceMethodDoAndTx(s.Name.Name, m)
		// 读取已有文件补充依赖 补充事务
		structStr += fmt.Sprintf("func (s *%s) %s(ctx context.Context, req *%s.%s) (res *%s.%s, err error) {\n\t"+
			"do := %s{\n%s\t}\n"+
			"\tdefer func(){\n\t\tif err!=nil{\n\t\t\ts.log.PrintError(err)\n\t\t}\n\t}()\n"+
			"%s"+
			"\treturn do.Do(ctx, req)\n}\n\n",
			s.Name.Name, m.Names[0].Name, g.PkgName, p.Name, g.PkgName, r.Name,
			m.Names[0].Name, fieldStr,
			txStr)
	}
	return structStr
}

// 服务方法填充 赋值及数据库事务
func (g *Generator) generateServiceMethodDoAndTx(serviceName string, field *ast.Field) (string, string) {
	var fieldStr bytes.Buffer
	var txStr bytes.Buffer
	for _, sm := range g.serMethodStructDoc {
		if sm.Name == field.Names[0].Name {
			//组装字段
			fields := sm.Decl.Specs[0].(*ast.TypeSpec).Type.(*ast.StructType).Fields.List
			var queryFields []string
			for _, fd := range fields {
				switch fd.Type.(type) {
				case *ast.StarExpr:
					expr := fd.Type.(*ast.StarExpr)
					if expr.X.(*ast.Ident).Name == serviceName {
						fieldStr.WriteString(fmt.Sprintf("\t\t%s: s,\n", serviceName))
					}
				case *ast.SelectorExpr:
					expr := fd.Type.(*ast.SelectorExpr)
					if expr.X.(*ast.Ident).Name == "query" {
						g.hasQuery = true
						queryFields = append(queryFields, expr.Sel.Name)
						fieldStr.WriteString(fmt.Sprintf("\t\t%s:query.%s{I%sDo:s.Query.%s.WithContext(ctx)},\n", expr.Sel.Name, expr.Sel.Name, expr.Sel.Name[:len(expr.Sel.Name)-3], expr.Sel.Name[:len(expr.Sel.Name)-3]))
					}
				}
			}
			//组装数据库事务
			if len(queryFields) > 0 {
				for _, method := range sm.Methods {
					if method.Name == "Do" {
						if strings.Contains(method.Doc, "@TX") {
							txStr.WriteString("\ttx := s.Db().Begin()\n")
							for _, queryField := range queryFields {
								txStr.WriteString(fmt.Sprintf("\tdo.%s.ReplaceDB(tx)\n", queryField))
							}
							txStr.WriteString("\tdefer query.RollBackFn(tx,s.Err,&err)\n")
						}
					}
				}
			}
			break
		}
	}
	return fieldStr.String(), txStr.String()
}

// 生成服务方法的结构体文件
func (g *Generator) generateMethodFile(intfs []*ast.TypeSpec) {
	g.refreshServiceMethodStructDoc()
	var infos []FileInfo
	for _, s := range intfs {
		//拿到方法名
		t := s.Type.(*ast.InterfaceType)
		for _, m := range t.Methods.List {
			// 需要检查文件是否已经生成 及 入参出参变化
			var find *doc.Type
			for _, sm := range g.serMethodStructDoc {
				if sm.Name == m.Names[0].Name {
					find = sm
					break
				}
			}
			//未找到新增
			if find == nil {
				var info FileInfo
				info.Name = m.Names[0].Name
				info.Buffer.WriteString("package " + g.DistPkg + "\n\n")
				info.Buffer.WriteString(g.generateMethodFileHeader())
				info.Buffer.WriteString(g.generateMethodFileStruct(s.Name.Name, m))
				info.Buffer.WriteString(g.generateMethodFileStructMethod(m))
				infos = append(infos, info)
			} else {
				file := g.DistPath + ToSnakeCase(m.Names[0].Name) + ".go"
				oldCode, err := ReadAll(g.DistPath + ToSnakeCase(m.Names[0].Name) + ".go")
				if err != nil {
					panic(err)
				}
				var newCode []byte
				for _, method := range find.Methods {
					if method.Name == "Do" {
						pOld := method.Decl.Type.Params.List
						pNew := m.Type.(*ast.FuncType).Params.List
						pOldExpr := pOld[1].Type.(*ast.StarExpr).X.(*ast.SelectorExpr)
						pNewExpr := pNew[1].Type.(*ast.StarExpr).X.(*ast.Ident)
						if pOldExpr.X.(*ast.Ident).Name != g.PkgName || pOldExpr.Sel.Name != pNewExpr.Name {
							//替换方法文件入参类型
							strCode := strings.Replace(string(oldCode), pOldExpr.X.(*ast.Ident).Name+"."+pOldExpr.Sel.Name, g.PkgName+"."+pNewExpr.Name, 1)
							newCode = []byte(strCode)
						}
						rOld := method.Decl.Type.Results.List
						rNew := m.Type.(*ast.FuncType).Results.List
						rOldExpr1 := rOld[0].Type.(*ast.StarExpr).X.(*ast.SelectorExpr)
						_, ok := rNew[0].Type.(*ast.StarExpr).X.(*ast.SelectorExpr)
						if ok {
							println(rNew[0].Type.(*ast.StarExpr).X)
						}
						rNewExpr1 := rNew[0].Type.(*ast.StarExpr).X.(*ast.Ident)
						if rOldExpr1.X.(*ast.Ident).Name != g.PkgName || rOldExpr1.Sel.Name != rNewExpr1.Name {
							//替换方法文件出参类型
							if newCode == nil {
								newCode = oldCode
							}
							strCode := strings.Replace(string(newCode), rOldExpr1.X.(*ast.Ident).Name+"."+rOldExpr1.Sel.Name, g.PkgName+"."+rNewExpr1.Name, 1)
							newCode = []byte(strCode)
						}
					}
				}
				if len(newCode) > 0 && string(oldCode) != string(newCode) {
					err = WriteByteFile(file, newCode)
					if err != nil {
						panic(err)
					}
				}
			}
		}
	}
	for _, mf := range infos {
		err := WriteByteFile(g.DistPath+ToSnakeCase(mf.Name)+".go", mf.Buffer.Bytes())
		if err != nil {
			panic(err)
		}
		log.Printf("generated method:%s \n", mf.Name)
	}
}

// 生成服务方法的头部
func (g *Generator) generateMethodFileHeader() string {
	var buffer bytes.Buffer
	for _, field := range g.MethodImports {
		buffer.WriteString("\n\t")
		buffer.WriteString(field)
	}
	structStr := fmt.Sprintf("import (\n\t\"context\"\n\t\"%s\"%s\n)\n\n", g.Model+"/rpc/kitex_gen/pb", buffer.String())
	return structStr
}

// 生成服务方法的结构体
func (g *Generator) generateMethodFileStruct(serviceName string, f *ast.Field) string {
	structStr := fmt.Sprintf("type %s struct {\n\t*%s\n}\n\n", f.Names[0].Name, serviceName)
	return structStr
}

// 生成服务方法的结构体
func (g *Generator) generateMethodFileStructMethod(m *ast.Field) string {
	f := m.Type.(*ast.FuncType)
	p := f.Params.List[1].Type.(*ast.StarExpr).X.(*ast.Ident)
	r := f.Results.List[0].Type.(*ast.StarExpr).X.(*ast.Ident)
	structStr := fmt.Sprintf("func (s *%s) Do(ctx context.Context, req *%s.%s) (res *%s.%s, err error) {\n\tpanic(\"implement me\")\n}\n", m.Names[0].Name, g.PkgName, p.Name, g.PkgName, r.Name)
	return structStr
}

func getImportPkg(pkg string) (string, error) {
	p, err := gobuild.Import(pkg, "", gobuild.FindOnly)
	if err != nil {
		return "", err
	}
	return p.Dir, err

}

func parseDir(dir, pkgName string) (*ast.Package, error) {
	pkgMap, err := goparser.ParseDir(
		token.NewFileSet(),
		dir,
		func(info os.FileInfo) bool {
			// skip go-test
			return !strings.Contains(info.Name(), "_test.go")
		},
		goparser.ParseComments, // no comment
	)
	if err != nil {
		return nil, err
	}

	pkg, ok := pkgMap[pkgName]
	if !ok {
		err := errors.New("not found")
		return nil, err
	}

	return pkg, nil
}

type visitor struct {
	funcs []*ast.FuncDecl
}

func (v *visitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		if n.Recv == nil ||
			!n.Name.IsExported() ||
			len(n.Recv.List) != 1 {
			return nil
		}
		t, ok := n.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			return nil
		}

		if t.X.(*ast.Ident).String() != "SugaredLogger" {
			return nil
		}

		log.Printf("func name: %s", n.Name.String())

		v.funcs = append(v.funcs, rewriteFunc(n))

	}
	return v
}

func walkAst(node ast.Node) ([]ast.Decl, error) {
	v := &visitor{}
	ast.Walk(v, node)

	log.Printf("funcs len: %d", len(v.funcs))

	var decls []ast.Decl
	for _, v := range v.funcs {
		decls = append(decls, v)
	}

	return decls, nil
}

func rewriteFunc(fn *ast.FuncDecl) *ast.FuncDecl {
	fn.Recv = nil

	fnName := fn.Name.String()

	var args []string
	for _, field := range fn.Type.Params.List {
		for _, id := range field.Names {
			idStr := id.String()
			_, ok := field.Type.(*ast.Ellipsis)
			if ok {
				// Ellipsis args
				idStr += "..."
			}
			args = append(args, idStr)
		}
	}

	exprStr := fmt.Sprintf("_globalS.%s(%s)", fnName, strings.Join(args, ","))
	expr, err := goparser.ParseExpr(exprStr)
	if err != nil {
		panic(err)
	}

	var body []ast.Stmt
	if fn.Type.Results != nil {
		body = []ast.Stmt{
			&ast.ReturnStmt{
				// Return:
				Results: []ast.Expr{expr},
			},
		}
	} else {
		body = []ast.Stmt{
			&ast.ExprStmt{
				X: expr,
			},
		}
	}

	fn.Body.List = body

	return fn
}

func astToGo(dst *bytes.Buffer, node interface{}) error {
	addNewline := func() {
		err := dst.WriteByte('\n') // add newline
		if err != nil {
			log.Panicln(err)
		}
	}

	addNewline()

	err := format.Node(dst, token.NewFileSet(), node)
	if err != nil {
		return err
	}

	addNewline()

	return nil
}

// Output Go code
func writeGoFile(wr io.Writer, funcs []ast.Decl) error {
	header := `// Code generated by log-gen. DO NOT EDIT.
package logx
`
	buffer := bytes.NewBufferString(header)

	for _, fn := range funcs {
		err := astToGo(buffer, fn)
		if err != nil {
			return err
		}
	}

	_, err := wr.Write(buffer.Bytes())
	return err
}
