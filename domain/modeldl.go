package domain

import (
	"fmt"
	"os"
	"sync"
	"time"

	"readalong/config"
	"readalong/model"
)

// 语音模型走 hf-mirror，国内直连 HuggingFace 基本不通。
var modelMirrors = []string{
	"https://hf-mirror.com",
	"https://huggingface.co",
}

const modelRepo = "ggerganov/whisper.cpp"

type modelEntry struct {
	model.ModelState
	mu sync.Mutex
}

var (
	modelMu    sync.Mutex
	modelCache = map[string]*modelEntry{}
)

func modelTracker(name string) *modelEntry {
	modelMu.Lock()
	defer modelMu.Unlock()
	e, ok := modelCache[name]
	if !ok {
		e = &modelEntry{ModelState: model.ModelState{Name: name, Msg: "未开始"}}
		modelCache[name] = e
	}
	return e
}

// ModelReady 判断模型文件是否已完整下载。
func ModelReady(name string) bool {
	st, err := os.Stat(config.ModelPath(name))
	return err == nil && !st.IsDir() && st.Size() > 1<<20
}

// ModelState 返回某个模型的当前状态。
func ModelState(name string) model.ModelState {
	e := modelTracker(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := e.ModelState
	cp.Ready = ModelReady(name)
	return cp
}

func (e *modelEntry) set(fn func(*model.ModelState)) {
	e.mu.Lock()
	fn(&e.ModelState)
	e.mu.Unlock()
}

// EnsureModelAsync 后台下载模型，重复调用不会并发下载。
func EnsureModelAsync(name string) {
	e := modelTracker(name)
	e.mu.Lock()
	if e.Downloading {
		e.mu.Unlock()
		return
	}
	e.Downloading = true
	e.Error = ""
	e.Msg = "准备下载…"
	e.mu.Unlock()

	go downloadModel(name)
}

func downloadModel(name string) {
	e := modelTracker(name)
	defer e.set(func(s *model.ModelState) { s.Downloading = false })

	if ModelReady(name) {
		e.set(func(s *model.ModelState) { s.Ready = true; s.Pct = 100; s.Msg = "模型已就绪" })
		return
	}
	if err := os.MkdirAll(config.ModelsDir, 0o755); err != nil {
		e.set(func(s *model.ModelState) { s.Error = err.Error() })
		return
	}

	dest := config.ModelPath(name)
	file := config.ModelFile(name)
	t0 := time.Now()
	var lastErr error

	for _, mirror := range modelMirrors {
		url := fmt.Sprintf("%s/%s/resolve/main/%s", mirror, modelRepo, file)
		e.set(func(s *model.ModelState) { s.Msg = "连接镜像…"; s.Pct = 0 })

		err := downloadFile(url, dest, func(done, total int64) {
			pct := 0
			if total > 0 {
				pct = int(done * 100 / total)
				if pct > 99 {
					pct = 99
				}
			}
			secs := time.Since(t0).Seconds()
			if secs < 0.1 {
				secs = 0.1
			}
			speed := float64(done) / 1048576 / secs
			e.set(func(s *model.ModelState) {
				s.Pct = pct
				s.DoneMB = float64(done) / 1048576
				s.TotalMB = float64(total) / 1048576
				s.Msg = fmt.Sprintf("下载模型 %.0fMB / %.0fMB · %.1fMB/s",
					float64(done)/1048576, float64(total)/1048576, speed)
			})
		})
		if err == nil && ModelReady(name) {
			e.set(func(s *model.ModelState) {
				s.Ready = true
				s.Pct = 100
				s.Msg = "模型已就绪"
			})
			return
		}
		lastErr = err
	}

	msg := "模型下载失败"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	e.set(func(s *model.ModelState) {
		s.Error = msg
		s.Msg = "模型下载失败"
		s.Pct = 0
	})
}
