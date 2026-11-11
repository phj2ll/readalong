package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"readalong/config"
	"readalong/model"
)

// 书的自定义信息（书名、封面）。一本书一个 books/<书ID>.json，
// 封面图也存在 config.BooksDir 下，文件名写进 json 的 cover 字段。

// 封面允许的扩展名（只做原样拷贝，不做重编码，避免引入图像库）。
var coverExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
	".gif":  true,
	".bmp":  true,
}

// maxCoverSize 限制封面体积，避免把几十 MB 的图塞进课程包。
const maxCoverSize = 12 << 20

func bookMetaPath(bookID string) string {
	return filepath.Join(config.BooksDir, bookID+".json")
}

// LoadBook 读一本书的自定义信息，没有就返回带 ID 的空值。
func LoadBook(bookID string) model.BookInfo {
	b := model.BookInfo{ID: bookID}
	raw, err := os.ReadFile(bookMetaPath(bookID))
	if err != nil {
		return b
	}
	if json.Unmarshal(raw, &b) != nil {
		return model.BookInfo{ID: bookID}
	}
	b.ID = bookID
	return b
}

func saveBook(b model.BookInfo) error {
	if !ValidID(b.ID) {
		return fmt.Errorf("书 id 不合法")
	}
	if err := os.MkdirAll(config.BooksDir, 0o755); err != nil {
		return err
	}
	return writeJSON(bookMetaPath(b.ID), b)
}

// Books 返回所有自定义过的书（有 json 的），供前端合并到课程库列表。
func Books() []model.BookInfo {
	out := []model.BookInfo{}
	entries, err := os.ReadDir(config.BooksDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !ValidID(id) {
			continue
		}
		b := LoadBook(id)
		if b.Cover != "" {
			if st, err := os.Stat(filepath.Join(config.BooksDir, b.Cover)); err == nil {
				b.Mtime = st.ModTime().Unix()
			} else {
				b.Cover = "" // json 里有、文件没了：当作没封面
			}
		}
		out = append(out, b)
	}
	return out
}

// CoverPath 返回封面文件的完整路径，没有封面时 ok=false。
func CoverPath(bookID string) (string, bool) {
	b := LoadBook(bookID)
	if b.Cover == "" {
		return "", false
	}
	p := filepath.Join(config.BooksDir, b.Cover)
	if !FileExists(p) {
		return "", false
	}
	return p, true
}

// RenameBook 改书名：写 books/<id>.json，同时把这本书下所有课程的
// meta.json 里的 bookTitle 一起改掉（课程库、导出、老逻辑都读那个字段）。
func RenameBook(bookID, title string) error {
	if !ValidID(bookID) {
		return fmt.Errorf("书 id 不合法")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("书名不能为空")
	}
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60])
	}
	b := LoadBook(bookID)
	b.Title = title
	if err := saveBook(b); err != nil {
		return err
	}

	entries, err := os.ReadDir(config.CoursesDir)
	if err != nil {
		return nil // 只有书信息也能改，课程读取失败不阻塞
	}
	for _, e := range entries {
		if !e.IsDir() || BookIDOf(e.Name()) != bookID {
			continue
		}
		metaPath := filepath.Join(config.CoursesDir, e.Name(), "meta.json")
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m model.CourseSummary
		if json.Unmarshal(raw, &m) != nil || m.ID == "" {
			continue
		}
		if m.BookTitle != title || m.BookID == "" {
			m.BookTitle = title
			if m.BookID == "" {
				m.BookID = bookID
			}
			_ = writeJSON(metaPath, m)
		}
		// data.json 里的 Course 也带 bookTitle，手机端导入后 bookOf() 优先读它，
		// 不同步的话 App 里会显示旧书名
		syncCourseBookTitle(filepath.Join(config.CoursesDir, e.Name(), "data.json"), title)
	}
	return nil
}

// syncCourseBookTitle 把课程 data.json 里的 bookTitle 改成 title。
func syncCourseBookTitle(path, title string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var data model.Course
	if json.Unmarshal(raw, &data) != nil || data.ID == "" || data.BookTitle == title {
		return
	}
	data.BookTitle = title
	_ = writeJSON(path, data)
}

// SetBookCover 把用户选的图片拷进 books 目录作为这本书的封面。
// 只做原样拷贝（不重编码），所以支持 jpg/png/webp/gif/bmp。
func SetBookCover(bookID, src string) (model.BookInfo, error) {
	if !ValidID(bookID) {
		return model.BookInfo{}, fmt.Errorf("书 id 不合法")
	}
	st, err := os.Stat(src)
	if err != nil || st.IsDir() {
		return model.BookInfo{}, fmt.Errorf("读取封面图片失败")
	}
	if st.Size() > maxCoverSize {
		return model.BookInfo{}, fmt.Errorf("封面图片太大了（超过 12MB）")
	}
	ext := strings.ToLower(filepath.Ext(src))
	if !coverExts[ext] {
		return model.BookInfo{}, fmt.Errorf("只支持 jpg / png / webp / gif / bmp 图片")
	}

	in, err := os.Open(src)
	if err != nil {
		return model.BookInfo{}, err
	}
	defer in.Close()

	if err := os.MkdirAll(config.BooksDir, 0o755); err != nil {
		return model.BookInfo{}, err
	}
	name := bookID + ext
	dst := filepath.Join(config.BooksDir, name)
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return model.BookInfo{}, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return model.BookInfo{}, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return model.BookInfo{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return model.BookInfo{}, err
	}

	b := LoadBook(bookID)
	if b.Cover != "" && b.Cover != name {
		_ = os.Remove(filepath.Join(config.BooksDir, b.Cover)) // 换了扩展名，删掉旧图
	}
	b.Cover = name
	if err := saveBook(b); err != nil {
		return b, err
	}
	if st, err := os.Stat(dst); err == nil {
		b.Mtime = st.ModTime().Unix()
	}
	return b, nil
}

// RemoveBook 删除一本书的自定义信息（删整本书时调用）。
func RemoveBook(bookID string) {
	if !ValidID(bookID) {
		return
	}
	if b := LoadBook(bookID); b.Cover != "" {
		_ = os.Remove(filepath.Join(config.BooksDir, b.Cover))
	}
	_ = os.Remove(bookMetaPath(bookID))
}
