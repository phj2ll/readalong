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

// Course 是一门完整课程，落盘为 data.json。
type Course struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Source   string    `json:"source"`
	Uploader string    `json:"uploader"`
	Duration float64   `json:"duration"`
	Audio    string    `json:"audio"`
	Lang     string    `json:"lang"`
	Created  string    `json:"created"`
	Segments []Segment `json:"segments"`
}

// CourseSummary 用于课程库列表，落盘为 meta.json。
type CourseSummary struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Source   string  `json:"source"`
	Uploader string  `json:"uploader"`
	Duration float64 `json:"duration"`
	Count    int     `json:"count"`
	Created  string  `json:"created"`
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

// Status 是应用启动时的整体状态。
type Status struct {
	Version string      `json:"version"`
	DataDir string      `json:"dataDir"`
	Model   ModelState  `json:"model"`
	Tools   []ToolState `json:"tools"`
}
