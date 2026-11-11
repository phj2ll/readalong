package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

// DeleteCourse 删除一门课程。删掉一本书的最后一门课时，顺手清掉它的书名/封面。
func (a *App) DeleteCourse(id string) error {
	if !store.ValidID(id) {
		return fmt.Errorf("课程 id 不合法")
	}
	if err := store.Delete(id); err != nil {
		return err
	}
	bookID := store.BookIDOf(id)
	for _, c := range store.List() {
		if store.BookIDOf(c.ID) == bookID {
			return nil // 这本书还有别的课，书信息保留
		}
	}
	store.RemoveBook(bookID)
	return nil
}

// ---------------- 书籍（书名 / 封面） ----------------

// ListBooks 返回用户改过书名或设过封面的书（没动过的书不出现在这里）。
func (a *App) ListBooks() []model.BookInfo { return store.Books() }

// RenameBook 改书名，并同步到这本书下所有课程的 meta（导出、Reader 都读那个字段）。
func (a *App) RenameBook(id, title string) error {
	if !store.ValidID(id) {
		return fmt.Errorf("书 id 不合法")
	}
	if err := store.RenameBook(id, title); err != nil {
		return err
	}
	logf("书名改为：%s（%s）", strings.TrimSpace(title), id)
	return nil
}

// PickBookCover 弹文件选择框，把选中的图片设为这本书的封面。
// 用户取消时返回当前的书籍信息（不报错）。
func (a *App) PickBookCover(id string) (model.BookInfo, error) {
	if !store.ValidID(id) {
		return model.BookInfo{}, fmt.Errorf("书 id 不合法")
	}
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return model.BookInfo{}, fmt.Errorf("窗口未就绪")
	}
	path, err := runtime.OpenFileDialog(ctx, runtime.OpenDialogOptions{
		Title: "选择书籍封面图片",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.jpg;*.png;*.webp;*.gif;*.bmp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp;*.gif;*.bmp"},
		},
	})
	if err != nil || path == "" {
		return store.LoadBook(id), err
	}
	info, err := store.SetBookCover(id, path)
	if err != nil {
		return model.BookInfo{}, err
	}
	logf("设置封面：%s（%s）", filepath.Base(path), id)
	return info, nil
}

// DownloadVideos 为已有课程补下载教学视频课（本地 mp4，离线播放）。
// 用于老课程（只有关联没有文件）或个别下载失败的视频。
func (a *App) DownloadVideos(id string) (model.Job, error) {
	if !store.ValidID(id) {
		return model.Job{}, fmt.Errorf("课程 id 不合法")
	}
	course, err := store.Load(id)
	if err != nil {
		return model.Job{}, fmt.Errorf("课程不存在：%w", err)
	}
	need := 0
	for _, v := range course.Videos {
		if v.File == "" {
			need++
		}
	}
	if need == 0 {
		return model.Job{}, fmt.Errorf("视频课都已下载到本地")
	}
	j := a.newJob()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				j.fail(fmt.Errorf("%v", r))
			}
		}()
		j.set(func(d *model.Job) { d.Step = "解析分 P 列表"; d.Pct = 2 })
		pages, err := domain.ListPages(course.Source)
		if err != nil {
			j.fail(fmt.Errorf("解析分 P 失败：%w", err))
			return
		}
		total := need
		span := 96 / total
		if span < 1 {
			span = 1
		}
		n := 0
		for i := range course.Videos {
			v := &course.Videos[i]
			if v.File != "" || v.Page-1 >= len(pages) {
				continue
			}
			cid := pages[v.Page-1].Cid
			name := fmt.Sprintf("video-%d.mp4", v.Page)
			j.set(func(d *model.Job) {
				d.Step = fmt.Sprintf("下载视频课（%d/%d）：%s", n+1, total, v.Title)
				d.Pct = 2 + (n+1)*span
			})
			dest := filepath.Join(config.CoursesDir, id, name)
			if err := domain.SaveVideo(course.Source, cid, dest); err != nil {
				j.log(fmt.Sprintf("P%d %s 下载失败：%v", v.Page, v.Title, err))
				continue
			}
			v.File = name
			n++
			if err := store.SaveData(course); err != nil {
				j.fail(fmt.Errorf("保存课程数据失败：%w", err))
				return
			}
		}
		if n == 0 {
			j.fail(fmt.Errorf("全部视频下载失败（多为网络/风控原因），详情见日志；稍后可再点一次补下载"))
			return
		}
		j.done(id, n)
	}()
	return j.snap(), nil
}

// ExportBook 把一本书（同一主链接的全部课程）打成一个课程包。
// 返回保存路径（取消返回空串）。
func (a *App) ExportBook(id string) (string, error) {
	if !store.ValidID(id) {
		return "", fmt.Errorf("课程 id 不合法")
	}
	bookID := store.BookIDOf(id)
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return "", fmt.Errorf("窗口未就绪")
	}
	title := store.BookTitle(bookID)
	path, err := runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{
		Title:           "导出课程包（整本书）",
		DefaultFilename: title + ".book.zip",
		Filters:         []runtime.FileFilter{{DisplayName: "ReadAlong 课程包 (*.zip)", Pattern: "*.zip"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := store.ZipBook(bookID, path); err != nil {
		return "", err
	}
	logf("导出课程包：%s（%s）", path, title)
	return path, nil
}

// ExportBookInfo 只导出书名和封面（小包，几百 KB）。改完书名/封面后用这个同步到手机，
// 不必重传整本书的音频和视频；手机端导入后只刷新书架上的书名和封面。
func (a *App) ExportBookInfo(id string) (string, error) {
	if !store.ValidID(id) {
		return "", fmt.Errorf("课程 id 不合法")
	}
	bookID := store.BookIDOf(id)
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return "", fmt.Errorf("窗口未就绪")
	}
	title := store.BookTitle(bookID)
	path, err := runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{
		Title:           "导出书名/封面（小包）",
		DefaultFilename: title + "-书信息.zip",
		Filters:         []runtime.FileFilter{{DisplayName: "ReadAlong 书信息包 (*.zip)", Pattern: "*.zip"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := store.ZipBookInfo(bookID, path); err != nil {
		return "", err
	}
	logf("导出书信息：%s（%s）", path, title)
	return path, nil
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

		// 没带 p= 参数的链接先探测是不是多 P；是就走批量，逐个 P 生成独立课程
		if !strings.Contains(rawURL, "p=") {
			j.set(func(d *model.Job) { d.Step = "解析分 P 列表"; d.Pct = 1 })
			pages, lerr := domain.ListPages(rawURL)
			if lerr != nil {
				j.log("分 P 探测失败，按单 P 生成：" + lerr.Error())
			} else if len(pages) > 1 {
				a.runBatch(j, rawURL, lang, pages)
				return
			}
		}

		res, err := domain.Run(rawURL, cid, lang, jobProgress{j: j})
		if err != nil {
			j.fail(err)
			return
		}
		res.Course.BookID = store.BaseBV(rawURL)
		if t := domain.SeriesOf(res.Course.Title); t != "" {
			res.Course.BookTitle = t
		} else {
			res.Course.BookTitle = res.Course.Title
		}
		defer os.Remove(res.WavPath) // 暂存的 wav，落盘（或失败）后都不再需要
		if err := store.Save(res.Course, res.WavPath); err != nil {
			j.fail(fmt.Errorf("保存课程失败：%w", err))
			return
		}
		j.done(cid, len(res.Course.Segments))
	}()

	return j.snap(), nil
}

// runBatch 把多 P 视频生成课程：标题带 Unit 的合并成一门课，其余每个 P 一门。
// 已存在的跳过，单个失败不影响后续。
func (a *App) runBatch(j *jobState, rawURL, lang string, pages []domain.PageInfo) {
	total := len(pages)
	span := 100 / total
	if span < 1 {
		span = 1
	}
	groups := domain.GroupPages(pages)
	// 视频课分 P（Let's start / explore 等教学动画）不适合跟读，跳过并在日志说明
	included := map[int]bool{}
	for _, g := range groups {
		for _, gp := range g.Pages {
			included[gp.Page] = true
		}
	}
	skipped := 0
	for i, pg := range pages {
		if !included[i+1] {
			skipped++
			j.log(fmt.Sprintf("跳过视频课 P%d %s（教学内容，不切句跟读）", i+1, pg.Title))
		}
	}
	if skipped > 0 {
		j.log(fmt.Sprintf("共 %d 个视频课分 P 不参与跟读生成", skipped))
	}
	ok, skip, fail := 0, 0, 0

	for _, g := range groups {
		cid := store.CourseIDForGroup(rawURL, g.Suffix)
		if store.CourseExists(cid) {
			skip += len(g.Pages)
			j.log(fmt.Sprintf("%s 已存在，跳过（要重新生成请先在课程库「删除整本」，或单独删掉这门课）", g.Label))
			continue
		}

		j.set(func(d *model.Job) {
			d.Step = fmt.Sprintf("%s 生成中（%d 个分 P）", g.Label, len(g.Pages))
		})
		op := offsetProgress{
			up:     jobProgress{j: j},
			prefix: g.Label,
			base:   (g.Pages[0].Page - 1) * span,
			span:   len(g.Pages) * span,
		}
		res, err := domain.RunGrouped(rawURL, cid, lang, g.Label, g.Pages, op)
		if err == nil {
			res.Course.BookID = store.BaseBV(rawURL)
			res.Course.BookTitle = domain.SeriesOf(domain.CleanTitle(pages[0].Title))
			// 教学视频课：下载成 mp4 放进课程目录（失败不影响课程，回退在线播放）
			for _, v := range g.Videos {
				ref := model.VideoRef{Title: v.Title, Page: v.Page}
				if v.Page-1 < len(pages) {
					c := pages[v.Page-1].Cid
					name := fmt.Sprintf("video-%d.mp4", v.Page)
					dest := filepath.Join(config.CoursesDir, cid, name)
					j.log(fmt.Sprintf("%s 下载视频课：%s", g.Label, v.Title))
					if err := domain.SaveVideo(rawURL, c, dest); err != nil {
						j.log(fmt.Sprintf("视频课下载失败（改为在线播放）：%v", err))
					} else {
						ref.File = name
					}
				}
				res.Course.Videos = append(res.Course.Videos, ref)
			}
		}
		if err != nil {
			fail += len(g.Pages)
			j.log(fmt.Sprintf("%s 失败：%v", g.Label, err))
			continue
		}
		if err := store.Save(res.Course, res.WavPath); err != nil {
			os.Remove(res.WavPath)
			fail += len(g.Pages)
			j.log(fmt.Sprintf("%s 保存失败：%v", g.Label, err))
			continue
		}
		os.Remove(res.WavPath)
		ok++
		j.log(fmt.Sprintf("%s 完成（%d 句，%d 个分 P）", g.Label, len(res.Course.Segments), len(g.Pages)))
	}

	if ok == 0 && skip == 0 {
		j.fail(fmt.Errorf("全部生成失败（共 %d 个分 P）", total))
		return
	}
	j.log(fmt.Sprintf("批量完成：课程 %d 门，跳过 %d 个分 P，失败 %d 个分 P", ok, skip, fail))
	j.set(func(d *model.Job) {
		d.Status = "done"
		d.Step = "完成"
		d.Pct = 100
		d.CourseID = "" // 批量不自动跳转，留在课程库看列表
	})
}

// offsetProgress 把一组分 P 的进度映射到整体进度条。
type offsetProgress struct {
	up     domain.Progress
	prefix string
	base   int
	span   int
}

func (p offsetProgress) Step(step string, pct int) {
	v := p.base + pct*p.span/100
	if v > 99 {
		v = 99
	}
	p.up.Step(p.prefix+" "+step, v)
}

func (p offsetProgress) Log(line string) { p.up.Log(p.prefix + " " + line) }

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

// ---------------- 语音评测（讯飞 ISE） ----------------

// GetSettings 返回评测凭据。
func (a *App) GetSettings() model.Settings { return store.LoadSettings() }

// SaveSettings 保存评测凭据，三项必须齐全。
func (a *App) SaveSettings(s model.Settings) error {
	s.AppID = strings.TrimSpace(s.AppID)
	s.APIKey = strings.TrimSpace(s.APIKey)
	s.APISecret = strings.TrimSpace(s.APISecret)
	if !store.SettingsReady(s) {
		return fmt.Errorf("APPID、APIKey、APISecret 都要填")
	}
	return store.SaveSettings(s)
}

// AssessRecording 把一段录音（webm 等，base64 编码）送讯飞评分。
// Wails 传参走 JSON，Uint8Array 会变成对象，所以前端编码成 base64 字符串。
func (a *App) AssessRecording(text, lang string, audioB64 string) (model.AssessResult, error) {
	audio, err := base64.StdEncoding.DecodeString(audioB64)
	if err != nil || len(audio) == 0 {
		return model.AssessResult{}, fmt.Errorf("录音数据无效")
	}
	st := store.LoadSettings()
	if !store.SettingsReady(st) {
		return model.AssessResult{}, fmt.Errorf("先到课程库右上角「评测设置」里填讯飞凭据")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return model.AssessResult{}, fmt.Errorf("参考文本为空")
	}
	pcm, err := domain.ConvertToPCM(audio)
	if err != nil {
		return model.AssessResult{}, err
	}
	res, err := domain.Assess(pcm, text, lang, st)
	if err != nil {
		logf("评测失败：%v", err)
		return model.AssessResult{}, err
	}
	logf("评测完成：总分 %.1f 准确 %.1f 流利 %.1f", res.Total, res.Accuracy, res.Fluency)
	return res, nil
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
