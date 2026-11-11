// Package config 统一解析运行期目录。
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 数据都放在用户目录，安装目录保持只读。
var (
	Root       string // 数据根目录
	CoursesDir string
	BooksDir   string
	ModelsDir  string
	TmpDir     string
	LogsDir    string
	BinDir     string // 运行期下载的外部程序
	ExeDir     string
)

func Init() {
	if exe, err := os.Executable(); err == nil {
		ExeDir = filepath.Dir(exe)
	} else {
		ExeDir, _ = os.Getwd()
	}

	if env := strings.TrimSpace(os.Getenv("READALONG_DATA")); env != "" {
		Root = env
	} else if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("APPDATA")
		}
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		Root = filepath.Join(base, "ReadAlong")
	} else {
		Root, _ = os.UserHomeDir()
		Root = filepath.Join(Root, ".readalong")
	}

	CoursesDir = filepath.Join(Root, "courses")
	// BooksDir 存每本书的元数据（书名、封面图），一本书一个 <书ID>.json
	BooksDir = filepath.Join(Root, "books")
	ModelsDir = filepath.Join(Root, "models")
	TmpDir = filepath.Join(Root, ".tmp")
	LogsDir = filepath.Join(Root, "logs")
	BinDir = filepath.Join(Root, "bin")

	for _, d := range []string{CoursesDir, BooksDir, ModelsDir, TmpDir, LogsDir, BinDir} {
		_ = os.MkdirAll(d, 0o755)
	}
}

// ModelName 按识别语言选模型：英文用 .en 版本，更准也更小。
func ModelName(lang string) string {
	if v := strings.TrimSpace(os.Getenv("READALONG_MODEL")); v != "" {
		return v
	}
	if lang == "en" || lang == "" {
		return "small.en"
	}
	return "small"
}

// DefaultModel 是启动时就预下载的模型。
const DefaultModel = "small.en"

// ModelFile 是 whisper.cpp 的 ggml 权重文件名。
func ModelFile(name string) string { return "ggml-" + name + ".bin" }

// ModelPath 返回模型在本地的完整路径。
func ModelPath(name string) string { return filepath.Join(ModelsDir, ModelFile(name)) }
