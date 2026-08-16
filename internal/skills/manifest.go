package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SkillMetadata struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

func ParseSkillMetadata(content []byte) SkillMetadata {
	text := string(content)
	text = strings.TrimPrefix(text, "\uFEFF")

	if !strings.HasPrefix(text, "---") {
		return SkillMetadata{}
	}

	rest := text[3:]
	if len(rest) > 0 && (rest[0] == '\n' || rest[0] == '\r') {
		rest = rest[1:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return SkillMetadata{}
	}

	frontmatter := rest[:endIdx]

	var meta SkillMetadata
	inDescription := false
	descriptionLines := []string{}
	flushDescription := func() {
		if len(descriptionLines) > 0 {
			meta.Description = strings.Join(descriptionLines, " ")
		}
	}
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)

		if inDescription {
			if trimmed == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				descriptionLines = append(descriptionLines, trimmed)
				continue
			}
			// 多行描述被新顶层字段打断：先保存已收集的描述再退出。
			// 否则 description 不是 frontmatter 最后一个字段时，收集的内容
			// 会因循环结束时 inDescription=false 而整体丢弃。
			flushDescription()
			inDescription = false
		}

		if !strings.Contains(line, ":") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		val := ""
		if len(parts) > 1 {
			val = parts[1]
		}

		switch strings.ToLower(key) {
		case "name":
			meta.Name = strings.Trim(strings.TrimSpace(val), "\"'")
		case "description":
			desc := strings.TrimSpace(val)
			if desc == "" || desc == "|" || desc == ">" || desc == "|-" || desc == ">-" {
				// 空值或 YAML 块标量指示符（|/>/|->-）：后续缩进行属于描述正文。
				// 块标量指示符本身是 "|" 而非空字符串，若按普通值处理会
				// 被解析为字面量 "|"，描述变成错误的单字符。
				inDescription = true
				descriptionLines = nil
			} else {
				meta.Description = strings.Trim(desc, "\"'")
				inDescription = false
			}
		default:
			if val == "" {
				inDescription = false
			}
		}
	}

	if inDescription {
		flushDescription()
	}

	return meta
}

func ReadSkillMetadata(dir string) (SkillMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return SkillMetadata{}, err
	}
	return ParseSkillMetadata(data), nil
}

func HasSkillManifest(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}

func ValidateDirectoryName(name string) error {
	if name == "" {
		return fmt.Errorf("directory name is empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("directory name cannot be '.' or '..'")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("directory name cannot contain path separators")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("directory name cannot start with '.'")
	}
	// 拒绝包含控制字符（含空字节、换行符等）的名称
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("directory name contains control character")
		}
	}
	// Windows 保留名（CON、PRN、AUX、NUL、COM1-9、LPT1-9）
	upper := strings.ToUpper(name)
	if isWindowsReservedName(upper) {
		return fmt.Errorf("directory name uses Windows reserved name: %s", upper)
	}
	return nil
}

// isWindowsReservedName 检查名称是否为 Windows 保留名
// 在所有平台上都拒绝，避免跨平台同步时出现问题
func isWindowsReservedName(upper string) bool {
	reserved := map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
	}
	if reserved[upper] {
		return true
	}
	// COM1-9, LPT1-9
	for i := 1; i <= 9; i++ {
		if upper == fmt.Sprintf("COM%d", i) || upper == fmt.Sprintf("LPT%d", i) {
			return true
		}
	}
	return false
}
