// Package store 负责课程数据的读写，一切以文件为准，不引入数据库。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"readalong/config"
	"readalong/model"
)

var (
	bvRe   = regexp.MustCompile(`BV([0-9A-Za-z]{10})`)
	idRe   = regexp.MustCompile(`^[0-9A-Za-z_.-]+$`)
	slugRe = regexp.MustCompile(`[^0-9A-Za-z_.-]`)
)

// baseID 只算稳定目录名，不处理已存在的情况。suffix 形如 p21 / u1。
func baseID(rawURL, suffix string) string {
	bv := "v"
	if m := bvRe.FindStringSubmatch(rawURL); m != nil {
		bv = "BV" + m[1]
	}
	base := bv
	if suffix != "" {
		base = bv + "_" + suffix
	}
	return slugRe.ReplaceAllString(base, "")
}

func pageSuffix(rawURL string) string {
	i := strings.Index(rawURL, "?")
	if i < 0 {
		return ""
	}
	q := rawURL[i+1:]
	if j := strings.Index(q, "#"); j >= 0 {
		q = q[:j]
	}
	for _, kv := range strings.Split(q, "&") {
		if strings.HasPrefix(kv, "p=") {
			return "p" + strings.TrimPrefix(kv, "p=")
		}
	}
	return ""
}

// CourseIDFromURL 生成稳定的课程目录名，同一个分 P 命中同一个目录。
func CourseIDFromURL(rawURL string) string {
	base := baseID(rawURL, pageSuffix(rawURL))

	cid := base
	for i := 2; DirExists(filepath.Join(config.CoursesDir, cid)) &&
		!FileExists(filepath.Join(config.CoursesDir, cid, "data.json")); i++ {
		cid = base + "_" + strconv.Itoa(i)
	}
	if FileExists(filepath.Join(config.CoursesDir, cid, "data.json")) {
		cid = base + "_" + time.Now().Format("150405")
	}
	return cid
}

// CourseIDForPage 按分 P 序号算目录名，给批量生成用，永远不带时间后缀。
func CourseIDForPage(rawURL string, page int) string {
	return baseID(rawURL, "p"+strconv.Itoa(page))
}

// CourseIDForGroup 按分组后缀（如 u1）算目录名。
func CourseIDForGroup(rawURL, suffix string) string {
	return baseID(rawURL, suffix)
}

// CourseExists 判断课程是否已经生成过。
func CourseExists(id string) bool {
	return FileExists(filepath.Join(config.CoursesDir, id, "data.json"))
}

// bookRe 从课程 ID 里提取书 ID（同一主链接的前缀，如 BV1qR4fesEm6_u1 → BV1qR4fesEm6）。
var bookRe = regexp.MustCompile(`^(BV[0-9A-Za-z]+)`)

// BookIDOf 从课程 ID 推导书 ID，老课程没有 bookId 字段时用它归组。
func BookIDOf(courseID string) string {
	if m := bookRe.FindStringSubmatch(courseID); m != nil {
		return m[1]
	}
	return courseID
}

// BaseBV 从链接里取 BV 号，作为书 ID。
func BaseBV(rawURL string) string {
	if m := bvRe.FindStringSubmatch(rawURL); m != nil {
		return "BV" + m[1]
	}
	return ""
}

// ValidID 校验课程/书籍 id，防目录穿越。
// 注意 "." 和 ".." 也满足 idRe（点号在白名单里），必须单独挡掉：
// 否则 DeleteCourse("..") 会 RemoveAll 掉整个数据根目录。
func ValidID(id string) bool {
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	return idRe.MatchString(id)
}

// Save 把课程写到磁盘：audio.wav + data.json + meta.json。
func Save(c model.Course, wavPath string) error {
	dir := filepath.Join(config.CoursesDir, c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	audio := filepath.Join(dir, "audio.wav")
	if FileExists(audio) {
		_ = os.Remove(audio)
	}
	if err := moveFile(wavPath, audio); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "data.json"), c); err != nil {
		return err
	}
	meta := model.CourseSummary{
		ID:        c.ID,
		Title:     c.Title,
		Source:    c.Source,
		Uploader:  c.Uploader,
		Duration:  c.Duration,
		Count:     len(c.Segments),
		Created:   c.Created,
		BookID:    c.BookID,
		BookTitle: c.BookTitle,
	}
	return writeJSON(filepath.Join(dir, "meta.json"), meta)
}

// SaveData 只重写课程的数据文件（不动音频），用于视频课补下载等增量更新。
func SaveData(c model.Course) error {
	dir := filepath.Join(config.CoursesDir, c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "data.json"), c)
}

// List 按创建时间倒序返回全部课程摘要。
func List() []model.CourseSummary {
	items := []model.CourseSummary{}
	entries, err := os.ReadDir(config.CoursesDir)
	if err != nil {
		return items
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(config.CoursesDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m model.CourseSummary
		if json.Unmarshal(raw, &m) == nil && m.ID != "" {
			items = append(items, m)
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Created > items[b].Created })
	return items
}

// Load 读取一门课程。
func Load(id string) (model.Course, error) {
	var c model.Course
	raw, err := os.ReadFile(filepath.Join(config.CoursesDir, id, "data.json"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}

// Delete 删除课程目录。
func Delete(id string) error {
	return os.RemoveAll(filepath.Join(config.CoursesDir, id))
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// FileExists 判断路径是否是可读文件。
func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// DirExists 判断目录是否存在。
func DirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
