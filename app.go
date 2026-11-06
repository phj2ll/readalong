package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"readalong/config"
	"readalong/domain"
	"readalong/model"
	"readalong/store"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是对前端暴露的方法集合，Wails 会把它的公开方法绑定成 JS 调用。
type App struct {
	mu   sync.Mutex
	ctx  context.Context
	jobs map[string]*jobState
}

type jobState struct {
	mu   sync.Mutex
	data model.Job
}

func NewApp() *App {
	return &App{jobs: map[string]*jobState{}}
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()

	logf("ReadAlong %s 启动，数据目录 %s", version, config.Root)
	if !domain.ModelReady(config.DefaultModel) {
		logf("语音模型未就绪，开始后台下载：%s", config.ModelPath(config.DefaultModel))
		domain.EnsureModelAsync(config.DefaultModel)
	}
}

func (a *App) shutdown(ctx context.Context) {
	logf("退出")
}

// showWindow 在第二个实例被拉起时把已有窗口顶到前面。
func (a *App) showWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	runtime.WindowUnminimise(ctx)
	runtime.WindowShow(ctx)
}

// ---------------- 状态 ----------------

// Version 返回运行时版本号（发布构建由 ldflags 注入）。
func (a *App) Version() string { return version }

// Status 返回启动所需的整体状态。
func (a *App) Status() model.Status {
	return model.Status{
		Version: version,
		DataDir: config.Root,
		Model:   domain.ModelState(config.DefaultModel),
		Tools:   domain.ToolStates(),
	}
}

// ModelState 返回语音模型下载状态，前端轮询它画进度条。
func (a *App) ModelState() model.ModelState {
	return domain.ModelState(config.DefaultModel)
}

// DownloadModel 手动触发模型下载。
func (a *App) DownloadModel() model.ModelState {
	domain.EnsureModelAsync(config.DefaultModel)
	return domain.ModelState(config.DefaultModel)
}

// ---------------- 课程 ----------------

// ListCourses 返回课程库。
func (a *App) ListCourses() []model.CourseSummary { return store.List() }

// CourseData 读取一门课程的完整数据。
func (a *App) CourseData(id string) (model.Course, error) {
	if !store.ValidID(id) {
		return model.Course{}, fmt.Errorf("课程 id 不合法")
	}
	return store.Load(id)
}

// DeleteCourse 删除一门课程。
func (a *App) DeleteCourse(id string) error {
	if !store.ValidID(id) {
		return fmt.Errorf("课程 id 不合法")
	}
	return store.Delete(id)
}

// CreateCourse 提交生成任务，立即返回任务快照，前端再轮询 Job。
func (a *App) CreateCourse(rawURL, lang string) (model.Job, error) {
	rawURL = strings.TrimSpace(rawURL)
	lang = normalizeLang(lang)
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return model.Job{}, fmt.Errorf("链接格式不对，要以 http 开头")
	}

	name := config.ModelName(lang)
	if !domain.ModelReady(name) {
		domain.EnsureModelAsync(name)
		return model.Job{}, fmt.Errorf("语音模型 %s 还在下载，下完再生成", config.ModelFile(name))
	}

	cid := store.CourseIDFromURL(rawURL)
	j := a.newJob()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf("任务异常：%v", r)
				j.fail(fmt.Errorf("%v", r))
			}
		}()

		res, err := domain.Run(rawURL, cid, lang, jobProgress{j: j})
		if err != nil {
			j.fail(err)
			return
		}
		if err := store.Save(res.Course, res.WavPath); err != nil {
			j.fail(fmt.Errorf("保存课程失败：%w", err))
			return
		}
		j.done(cid, len(res.Course.Segments))
	}()

	return j.snap(), nil
}

// Job 查询任务进度。
func (a *App) Job(id string) (model.Job, error) {
	a.mu.Lock()
	j, ok := a.jobs[id]
	a.mu.Unlock()
	if !ok {
		return model.Job{}, fmt.Errorf("没有这个任务")
	}
	return j.snap(), nil
}

// ---------------- 外部动作 ----------------

// OpenDataDir 在资源管理器里打开数据目录。
func (a *App) OpenDataDir() error {
	dir := config.Root
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	if isWindows() {
		return exec.Command("explorer", dir).Start()
	}
	return exec.Command("xdg-open", dir).Start()
}

// OpenURL 用默认浏览器打开链接（比如原视频地址）。
func (a *App) OpenURL(u string) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return
	}
	runtime.BrowserOpenURL(ctx, u)
}

// ---------------- 任务实现 ----------------

func (a *App) newJob() *jobState {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	j := &jobState{data: model.Job{
		ID:     hex.EncodeToString(b),
		Status: "running",
		Step:   "排队中",
		Log:    []string{},
	}}
	a.mu.Lock()
	a.jobs[j.data.ID] = j
	a.mu.Unlock()
	return j
}

func (j *jobState) set(fn func(*model.Job)) {
	j.mu.Lock()
	fn(&j.data)
	j.mu.Unlock()
}

func (j *jobState) log(line string) {
	j.mu.Lock()
	j.data.Log = append(j.data.Log, line)
	if len(j.data.Log) > 40 {
		j.data.Log = j.data.Log[len(j.data.Log)-40:]
	}
	id := j.data.ID
	j.mu.Unlock()
	logf("[%s] %s", id[:6], line)
}

func (j *jobState) snap() model.Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	cp := j.data
	cp.Log = append([]string{}, j.data.Log...)
	return cp
}

func (j *jobState) done(courseID string, count int) {
	j.log(fmt.Sprintf("完成：%d 句", count))
	j.set(func(d *model.Job) {
		d.Status = "done"
		d.Step = "完成"
		d.Pct = 100
		d.CourseID = courseID
	})
}

func (j *jobState) fail(err error) {
	j.log("失败：" + err.Error())
	j.set(func(d *model.Job) {
		d.Status = "error"
		d.Step = "失败"
		d.Error = err.Error()
	})
}

type jobProgress struct{ j *jobState }

func (p jobProgress) Step(step string, pct int) {
	p.j.set(func(d *model.Job) {
		d.Step = step
		d.Pct = pct
	})
}

func (p jobProgress) Log(line string) { p.j.log(line) }

func normalizeLang(lang string) string {
	switch strings.TrimSpace(lang) {
	case "zh", "auto":
		return strings.TrimSpace(lang)
	default:
		return "en"
	}
}

func logf(format string, args ...any) {
	log.Printf(format, args...)
}
