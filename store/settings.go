package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	"readalong/config"
	"readalong/model"
)

// SettingsPath 是评测凭据的落盘位置。
func SettingsPath() string { return filepath.Join(config.Root, "settings.json") }

// LoadSettings 读取讯飞评测凭据，没有文件就返回零值。
func LoadSettings() model.Settings {
	var s model.Settings
	b, err := os.ReadFile(SettingsPath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// SaveSettings 写入评测凭据（覆盖）。
func SaveSettings(s model.Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(SettingsPath(), b, 0o600)
}

// SettingsReady 判断评测凭据是否已配置完整。
func SettingsReady(s model.Settings) bool {
	return s.AppID != "" && s.APIKey != "" && s.APISecret != ""
}
