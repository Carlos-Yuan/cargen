package gen

import (
	"bytes"

	"fmt"
	"go/ast"
	"go/doc"

	"regexp"
	"strings"

	"github.com/carlos-yuan/cargen/util/convert"
	"github.com/carlos-yuan/cargen/util/fileUtil"
)

func ModelToProtobuf(path, protoPkg, goPkg, modelPath, modelName string) {
	pkg, err := parseDir(modelPath, modelName)
	if err != nil {
		panic(err)
	}
	pkgs := doc.New(pkg, modelPath, doc.AllMethods)
	var protoBuf bytes.Buffer
	protoBuf.WriteString(fmt.Sprintf("// Code generated by car-gen. DO NOT EDIT.\n\nsyntax = \"proto3\";\n\npackage %s;\n\noption go_package = \"%s\";\n\n", protoPkg, goPkg))
	for _, t := range pkgs.Types {
		if strings.Contains(t.Doc, "mapped from table") && len(t.Decl.Specs) == 1 {
			list := t.Decl.Specs[0].(*ast.TypeSpec).Type.(*ast.StructType).Fields.List
			var i = 1
			protoBuf.WriteString(fmt.Sprintf("message %sRsp {\n", t.Name))
			for _, field := range list {
				name := field.Names[0].Name
				typ := ""
				switch field.Type.(type) {
				case *ast.Ident:
					typ = field.Type.(*ast.Ident).Name
				case *ast.SelectorExpr:
					typ = field.Type.(*ast.SelectorExpr).X.(*ast.Ident).Name + "." + field.Type.(*ast.SelectorExpr).Sel.Name
				case *ast.StarExpr:
					switch field.Type.(*ast.StarExpr).X.(type) {
					case *ast.Ident:
						typ = field.Type.(*ast.StarExpr).X.(*ast.Ident).Name
					case *ast.SelectorExpr:
						typ = field.Type.(*ast.StarExpr).X.(*ast.SelectorExpr).X.(*ast.Ident).Name + "." + field.Type.(*ast.StarExpr).X.(*ast.SelectorExpr).Sel.Name
					}
				}
				if !(name == "CreateBy" || name == "DeletedAt" || name == "UpdateBy") {
					if typ == "time.Time" {
						typ = "string"
					}
					switch typ {
					case "float64":
						typ = "double"
					case "float32":
						typ = "float"
					case "time.Time":
						typ = "string"
					}
					if name != "ID" {
						name = convert.FistToLower(name)
					}
					reg := regexp.MustCompile("`gorm:(.*);comment:(.*)\"(.*)json:(.*)`")
					params := reg.FindStringSubmatch(field.Tag.Value)
					var comment string
					if len(params) == 5 {
						comment = params[2]
					} else {
						comment = name
					}
					protoBuf.WriteString(fmt.Sprintf("\t%s %s =%d;//%s\n", typ, name, i, comment))
					i++
				}
			}
			protoBuf.WriteString("}\n\n")
		}
	}
	dir := path + "/" + protoPkg + "/rpc/" + protoPkg + "_model_gen.proto"
	err = fileUtil.WriteByteFile(dir, protoBuf.Bytes())
	if err != nil {
		panic(err)
	}
}
