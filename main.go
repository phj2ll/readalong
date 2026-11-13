package main

import (
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"readalong/config"
	"readalong/store"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// version 是运行时版本号：发布构建由 CI 通过 -ldflags "-X main.version=<版本>" 注入。
var version = "dev"

func main() {
	config.Init()
	setupLog()

	sub, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}

	app := NewApp()
	err = wails.Run(&options.App{
		Title:     "一起读 · 跟读课程生成器",
		Width:     1180,
		Height:    860,
		MinWidth:  960,
		MinHeight: 620,
		// 课程音频走自定义协议，前端才能用 <audio> 拖动进度
		AssetServer: &assetserver.Options{
			Assets:  sub,
			Handler: newAssetHandler(sub),
		},
		BackgroundColour: &options.RGBA{R: 247, G: 247, B: 245, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "readalong-2f1c9b",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				app.showWindow()
			},
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// newAssetHandler 把 /courses/* 和 /books/* 指到数据目录，其余交给前端静态资源。
func newAssetHandler(assetsFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assetsFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/courses/") {
			serveCourseFile(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/books/") {
			serveBookFile(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveBookFile 提供书籍封面（books 目录下的单层文件，如 BV1xxx.jpg）。
func serveBookFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/books/")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	if id := strings.TrimSuffix(name, filepath.Ext(name)); !store.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(config.BooksDir, name)
	if !store.FileExists(p) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, p)
}

func serveCourseFile(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/courses/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, name := parts[0], parts[1]
	if !store.ValidID(id) || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(config.CoursesDir, id, name)
	if !store.FileExists(p) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, p)
}

func setupLog() {
	f, err := os.OpenFile(filepath.Join(config.LogsDir, "readalong.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

func isWindows() bool { return runtime.GOOS == "windows" }
