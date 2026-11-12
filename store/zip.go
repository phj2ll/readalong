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
// Cover 是封面在包内的文件名（如 cover.jpg），空表示这本书没有封面。
// InfoOnly=true 表示这是「只带了书名和封面」的小包，手机端只刷新书架信息。
type bookManifest struct {
	ID       string                `json:"id"`
	Title    string                `json:"title"`
	Cover    string                `json:"cover"`
	InfoOnly bool                  `json:"infoOnly,omitempty"`
	Course   []model.CourseSummary `json:"courses"`
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
				// 「·」占 2 字节，别只 +1（会把 0xB7 留在书名开头）
				title = strings.TrimSpace(m.Title[i+len("·"):])
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
func ZipBook(bookID, dest string) (err error) {
	summary, _, err := bookCourses(bookID)
	if err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	// 中途失败时清掉半截 zip，别在用户选的路径上留一个打不开的文件
	defer func() {
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			_ = os.Remove(dest)
		}
	}()

	write := func(name string, b []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}

	// 封面图一并打进包里，手机端导入后书架直接显示（没有就跳过）
	coverName, coverBytes := coverOf(bookID)

	manifest := bookManifest{ID: bookID, Title: BookTitle(bookID), Cover: coverName, Course: summary}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := write("book.json", mb); err != nil {
		return err
	}
	if coverName != "" {
		if err := write(coverName, coverBytes); err != nil {
			return err
		}
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

	if cerr := zw.Close(); cerr != nil {
		return cerr
	}
	return f.Close()
}

// coverOf 取出封面的包内文件名和字节内容，没有封面返回空。
func coverOf(bookID string) (string, []byte) {
	p, ok := CoverPath(bookID)
	if !ok {
		return "", nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", nil
	}
	return "cover" + strings.ToLower(filepath.Ext(p)), b
}

// ZipBookInfo 只打包书名和封面（通常几百 KB），用于改完书名/封面后快速同步到手机，
// 不必重传整本书的音频和视频。手机端识别 infoOnly 后只刷新书架信息。
func ZipBookInfo(bookID, dest string) (err error) {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	// 中途失败时清掉半截 zip
	defer func() {
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			_ = os.Remove(dest)
		}
	}()
	write := func(name string, b []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}

	coverName, coverBytes := coverOf(bookID)
	manifest := bookManifest{
		ID:       bookID,
		Title:    BookTitle(bookID),
		Cover:    coverName,
		InfoOnly: true,
		Course:   []model.CourseSummary{},
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := write("book.json", mb); err != nil {
		return err
	}
	if coverName != "" {
		if err := write(coverName, coverBytes); err != nil {
			return err
		}
	}
	if cerr := zw.Close(); cerr != nil {
		return cerr
	}
	return f.Close()
}

// BookTitle 返回书名（导出文件名用）。用户在课程库里改过书名就以 books/<id>.json 为准。
func BookTitle(bookID string) string {
	if b := LoadBook(bookID); strings.TrimSpace(b.Title) != "" {
		return b.Title
	}
	_, title, err := bookCourses(bookID)
	if err != nil || title == "" {
		return bookID
	}
	return title
}
