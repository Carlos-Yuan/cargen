package gen

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	openapi "github.com/carlos-yuan/cargen/open_api"
	"github.com/carlos-yuan/cargen/util/convert"
	"github.com/carlos-yuan/cargen/util/fileUtil"
)

// CreateApiRouter 生成api路由
func CreateApiRouter(genPath string) {
	pkgs := openapi.Packages{}
	pkgs.Init(genPath)
	var routers = make(map[string]string) //map[文件路径]代码
	for _, pkg := range pkgs {
		for _, s := range pkg.Structs {
			sort.Slice(s.Api, func(i, j int) bool {
				return strings.Compare(s.Api[i].GetRequestPath(), s.Api[j].GetRequestPath()) == 1
			})
			if len(s.Api) > 0 {
				path := pkg.ModPath + "/router/" + convert.ToSnakeCase(s.Name) + ".gen.go"
				importInfo := convert.LastName(pkg.Path)
				if importInfo == pkg.Name {
					importInfo = `"` + pkg.Path + `"`
				} else {
					importInfo += ` "` + pkg.Path + `"`
				}
				var apiWriter bytes.Buffer
				for _, api := range s.Api {
					checkToken := ""
					if api.Auth != "" { //鉴权加载
						auth := api.Auth
						if api.AuthTo != "" && api.AuthTo != api.Auth {
							auth += ":" + api.AuthTo
						}
						checkToken = "\n\t\t\t\tt.CheckToken(tokenMap[`" + auth + "`])"
					}
					urlPath := api.GetRequestPathNoGroup()
					if api.Params != nil {
						for _, param := range api.Params.Fields {
							if param.In == openapi.OpenApiInPath {
								urlPath = strings.ReplaceAll(urlPath, "{"+param.ParamName+"}", ":"+param.ParamName)
							}
						}
					}
					//t.Success([]byte)纯二进制返回判断
					isByteArray := false
					for _, f := range api.Response.Fields {
						if f.Name == "Data" && f.Type == "byte" && f.Array {
							isByteArray = true
						}
					}
					if !isByteArray {
						apiWriter.WriteString(fmt.Sprintf("\n\t\t\tctl.GinRegister{Method: \"%s\", Path: prefix + \"%s\", Handles: []gin.HandlerFunc{func(ctx *gin.Context) {"+
							"\n\t\t\t\tt := t.SetContext(ctx)"+
							"%s"+ //鉴权
							"\n\t\t\t\tctx.JSON(200, t.%s())"+
							"\n\t\t\t}}},",
							strings.ToUpper(api.HttpMethod),
							urlPath,
							checkToken,
							api.Name,
						))
					} else {
						apiWriter.WriteString(fmt.Sprintf("\n\t\t\tctl.GinRegister{Method: \"%s\", Path: prefix + \"%s\", Handles: []gin.HandlerFunc{func(ctx *gin.Context) {"+
							"\n\t\t\t\tt := t.SetContext(ctx)"+
							"%s"+ //鉴权
							"\n\t\t\t\tres := t.%s()"+ //鉴权
							"\n\t\t\t\tctx.Writer.WriteHeader(res.Code)"+
							"\n\t\t\t\tctx.Writer.Write(res.Data.([]byte))"+
							"\n\t\t\t}}},",
							strings.ToUpper(api.HttpMethod),
							urlPath,
							checkToken,
							api.Name,
						))
					}
				}
				routers[path] = fmt.Sprintf(apiRouterTemplate, "\t"+importInfo, pkg.Name+"."+s.Name, apiWriter.String())
			}
		}
	}
	for path, src := range routers {
		err := fileUtil.WriteByteFile(path, []byte(src))
		if err != nil {
			panic(err)
		}
	}
}

const apiRouterTemplate = `// Code generated by car-gen. DO NOT EDIT.
// Code generated by car-gen. DO NOT EDIT.
// Code generated by car-gen. DO NOT EDIT.
package router

import (
	"github.com/carlos-yuan/cargen/core/config"
	ctl "github.com/carlos-yuan/cargen/core/controller"
	"github.com/carlos-yuan/cargen/util/convert"
	"github.com/gin-gonic/gin"
	"strings"
%s
)

func init() {
	err := config.Container.Invoke(func(t *%s, c *config.Config) {
		mod, name := convert.GetStructModAndName(t)
		t.ControllerContext = ctl.NewGinContext(c.Web[mod])
		prefix := c.Web[mod].Prefix + strings.ToLower(mod) + "/" + convert.FistToLower(name)
		routerList = append(routerList,
%s
		)
	})
	if err != nil {
		panic(err.Error())
	}
}
`
