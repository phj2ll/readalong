package store

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"readalong/config"
	"readalong/model"
)

// bookManifest 是课程包的清单，Reader 导入时按它还原整本书。
type bookManifest struct {
	ID     string               `json:"id"`
	Title  string               `json:"title"`
	Course []model.CourseSummary `json:"courses"`
}

// bookCourses 找出属于一本书的全部课程摘要（含老课程按 ID 前缀归组）。
func bookCourses(bookID string) ([]model.CourseSummary, string, error) {
	entries, err := os.ReadDir(config.CoursesDir)
	if err != nil {
		return nil, "", err
	}
	var out []model.CourseSummary
	title := ""
	for _, e := range entries {
		if !e.IsDir() || BookIDOf(e.Name()) != bookID {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(config.CoursesDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m model.CourseSummary
		if json.Unmarshal(raw, &m) != nil || m.ID == "" {
			continue
		}
		if m.BookID == "" {
			m.BookID = bookID
		}
		if m.BookTitle != "" && title == "" {
			title = m.BookTitle
		}
		if title == "" {
			// 从课程标题的「·」后面兜底提取书名
			if i := strings.LastIndex(m.Title, "·"); i >= 0 {
				title = strings.TrimSpace(m.Title[i+1:])
			}
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("这本书下没有课程")
	}
	if title == "" {
		title = bookID
	}
	return out, title, nil
}

// ZipBook 把一本书的全部课程打成一个课程包（一次导出、一次导入）。
// 结构：book.json + <课程ID>/data.json + <课程ID>/audio.wav
func ZipBook(bookID, dest string) error {
	summary, bookTitle, err := bookCourses(bookID)
	if err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	write := func(name string, b []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}

	manifest := bookManifest{ID: bookID, Title: bookTitle, Course: summary}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := write("book.json", mb); err != nil {
		return err
	}

	// 把当前评测凭据一并打进课程包：手机端导入后自动写入本地配置，
	// 用户不用在手机上手动再填一遍（凭据留在 zip 里，别外传课程包）。
	if st := LoadSettings(); SettingsReady(st) {
		if sb, err := json.MarshalIndent(st, "", "  "); err == nil {
			if err := write("app-settings.json", sb); err != nil {
				return err
			}
		}
	}

	for _, m := range summary {
		dir := filepath.Join(config.CoursesDir, m.ID)
		// data.json + audio.wav + video-*.mp4（教学视频课，有的话）
		names := []string{"data.json", "audio.wav"}
		if ents, err := os.ReadDir(dir); err == nil {
			for _, e := range ents {
				if !e.IsDir() && strings.HasPrefix(e.Name(), "video-") && strings.HasSuffix(e.Name(), ".mp4") {
					names = append(names, e.Name())
				}
			}
		}
		for _, name := range names {
			src, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				if name != "audio.wav" && name != "data.json" {
					continue // 视频缺失不阻塞导出
				}
				return fmt.Errorf("课程 %s 缺少 %s：%w", m.ID, name, err)
			}
			w, err := zw.Create(m.ID + "/" + name)
			if err == nil {
				_, err = io.Copy(w, src)
			}
			src.Close()
			if err != nil {
				return err
			}
		}
	}

	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

// BookTitle 返回书名（导出文件名用）。
func BookTitle(bookID string) string {
	_, title, err := bookCourses(bookID)
	if err != nil || title == "" {
		return bookID
	}
	return title
}
