// Command heatx-server 启动间壁式换热器工况核算 HTTP 服务。
//
// 端口由环境变量 HEATX_PORT 指定,默认 8080。
package main

import (
	"log"
	"net/http"
	"os"

	"heatx/internal/heatx"
)

func main() {
	port := os.Getenv("HEATX_PORT")
	if port == "" {
		port = "8080"
	}

	handler := heatx.Handler(heatx.DefaultServerConfig())
	addr := ":" + port
	log.Printf("heatx 换热器核算服务启动,监听 %s (GET /api/v1/config 查看能力)", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
