// Package model 存放前后端共用的数据结构。
package model

// Segment 是一句跟读单元。
type Segment struct {
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	Text         string  `json:"text"`
	Section      string  `json:"section,omitempty"`
	SectionStart float64 `json:"section_start,omitempty"`
}

// VideoRef 是关联的教学视频课分 P。
// File 非空表示已下载到课程目录（离线播放）；为空则回退在线嵌入播放。
type VideoRef struct {
	Title string `json:"title"`
	Page  int    `json:"page"` // 视频在原视频里的分 P 序号
	File  string `json:"file,omitempty"`
}

// Course 是一门完整课程，落盘为 data.json。
// BookID/BookTitle 是所属「书」（同一主链接生成的全部课程），课程库按书分组。
type Course struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Source    string     `json:"source"`
	Uploader  string     `json:"uploader"`
	Duration  float64    `json:"duration"`
	Audio     string     `json:"audio"`
	Lang      string     `json:"lang"`
	Created   string     `json:"created"`
	BookID    string     `json:"bookId,omitempty"`
	BookTitle string     `json:"bookTitle,omitempty"`
	Videos    []VideoRef `json:"videos,omitempty"`
	Segments  []Segment  `json:"segments"`
	// AlignVersion 是单词页时间轴的校正版本，老课程启动时会按音频重做一次。
	AlignVersion int `json:"alignVersion,omitempty"`
}

// CourseSummary 用于课程库列表，落盘为 meta.json。
type CourseSummary struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Source    string  `json:"source"`
	Uploader  string  `json:"uploader"`
	Duration  float64 `json:"duration"`
	Count     int     `json:"count"`
	Created   string  `json:"created"`
	BookID    string  `json:"bookId,omitempty"`
	BookTitle string  `json:"bookTitle,omitempty"`
}

// Job 描述一次生成任务。
type Job struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"` // running | done | error
	Step     string   `json:"step"`
	Pct      int      `json:"pct"`
	Log      []string `json:"log"`
	CourseID string   `json:"courseId"`
	Error    string   `json:"error"`
	// Replaced 表示这门课的链接已经生成过、这次是覆盖旧课程，
	// 前端据此清掉旧的练习进度/评分/录音（时间轴已经变了，留着会错位）。
	Replaced bool `json:"replaced,omitempty"`
}

// ModelState 是语音模型下载状态，前端顶部进度条用。
type ModelState struct {
	Name        string  `json:"name"`
	Ready       bool    `json:"ready"`
	Downloading bool    `json:"downloading"`
	Pct         int     `json:"pct"`
	DoneMB      float64 `json:"doneMB"`
	TotalMB     float64 `json:"totalMB"`
	Msg         string  `json:"msg"`
	Error       string  `json:"error"`
}

// ToolState 汇总外部程序的就绪情况（yt-dlp / ffmpeg / whisper）。
type ToolState struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Ready   bool   `json:"ready"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// BookInfo 是一本书的自定义信息（书名、封面），落盘为 books/<书ID>.json。
// 没有这个文件的书籍走「从课程 meta 推导书名」的老逻辑。
type BookInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Cover string `json:"cover"` // 封面文件名（books 目录内），空表示没有封面
	Mtime int64  `json:"mtime"` // 封面文件修改时间，前端拿来做 ?v= 破缓存
}

// Settings 是讯飞语音评测的凭据，落盘为 settings.json。
type Settings struct {
	AppID     string `json:"appId"`
	APIKey    string `json:"apiKey"`
	APISecret string `json:"apiSecret"`
}

// AssessResult 是一次跟读评分（各项满分 100）。
type AssessResult struct {
	Total     float64 `json:"total"`
	Accuracy  float64 `json:"accuracy"`
	Fluency   float64 `json:"fluency"`
	Integrity float64 `json:"integrity"`
}

// Status 是应用启动时的整体状态。
type Status struct {
	Version string      `json:"version"`
	DataDir string      `json:"dataDir"`
	Model   ModelState  `json:"model"`
	Tools   []ToolState `json:"tools"`
}
