package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"model-proxy-go/internal/api"
	"model-proxy-go/internal/db"
	"model-proxy-go/internal/proxy"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	// 初始化 SQLite（使用绝对路径，避免工作目录依赖）
	execPath, _ := os.Executable()
	dbPath := filepath.Join(filepath.Dir(execPath), "data", "proxy.db")
	if err := db.Init(dbPath); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()

	// 静态文件
	sub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// 代理转发: /api/{proxyCode}/**
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := r.Method

		// SSE 流
		if path == "/api/logs/stream" {
			proxy.SSEHandler(proxy.SSEHub)(w, r)
			return
		}

		// 代理配置 API
		if strings.HasPrefix(path, "/api/config/") {
			handleConfigAPI(w, r, path, method)
			return
		}

		// 请求日志 API
		if strings.HasPrefix(path, "/api/log/") {
			handleLogAPI(w, r, path, method)
			return
		}

		// 训练数据导出
		if path == "/api/export" {
			api.ExportHandler(w, r)
			return
		}

		// 统计
		if path == "/api/stats" {
			api.Stats(w, r)
			return
		}

		// 否则是代理转发
		proxy.Handler()(w, r)
	})

	log.Println("Model Proxy 启动: http://localhost:8089")
	log.Println("数据库: data/proxy.db (SQLite)")

	server := &http.Server{
		Addr:         "localhost:8089",
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 600 * time.Second, // SSE 长时间流 + LLM 生成可能很长
		IdleTimeout:  120 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func handleConfigAPI(w http.ResponseWriter, r *http.Request, path, method string) {
	switch {
	case path == "/api/config/list" && method == "GET":
		api.ConfigList(w, r)
	case path == "/api/config/add" && method == "POST":
		api.ConfigAdd(w, r)
	case path == "/api/config/update" && method == "POST":
		api.ConfigUpdate(w, r)
	case strings.HasPrefix(path, "/api/config/delete/") && method == "DELETE":
		api.ConfigDelete(w, r)
	default:
		http.NotFound(w, r)
	}
}

func handleLogAPI(w http.ResponseWriter, r *http.Request, path, method string) {
	switch {
	case path == "/api/log/list" && method == "GET":
		api.LogList(w, r)
	case strings.HasPrefix(path, "/api/log/payload/") && method == "GET":
		api.LogPayload(w, r)
	case strings.HasPrefix(path, "/api/log/delete/") && method == "DELETE":
		api.LogDelete(w, r)
	default:
		http.NotFound(w, r)
	}
}
