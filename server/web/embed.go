// Package web 内嵌前端静态文件(原生 JS，无构建步骤)。
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(files, "static")
	return sub
}
