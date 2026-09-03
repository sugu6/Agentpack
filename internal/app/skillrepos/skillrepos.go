// Package skillrepos 提供技能仓库（GitHub Repo）列表的纯逻辑管理：
// 添加/移除/修改/查找，不依赖 App 状态，可独立测试。
// 持久化（config.Save）与锁编排由根目录 app.go 负责。
package skillrepos

import (
	"fmt"

	"agentpack/internal/config"
)

// normalize 补齐默认分支（空则用 main）。
func normalize(repo *config.SkillRepo) {
	if repo.Branch == "" {
		repo.Branch = "main"
	}
}

// has 判断列表中是否已存在 owner/name 相同的仓库。
func has(list []config.SkillRepo, owner, name string) bool {
	for _, r := range list {
		if r.Owner == owner && r.Name == name {
			return true
		}
	}
	return false
}

// Add 追加一个仓库；返回新列表与错误。重复/缺 owner-name 时报错。
func Add(list []config.SkillRepo, repo config.SkillRepo) ([]config.SkillRepo, error) {
	if repo.Owner == "" || repo.Name == "" {
		return list, fmt.Errorf("repo owner and name required")
	}
	normalize(&repo)
	if has(list, repo.Owner, repo.Name) {
		return list, fmt.Errorf("repo %s/%s already exists", repo.Owner, repo.Name)
	}
	return append(list, repo), nil
}

// Remove 按 owner/name 移除一个仓库。返回新列表与是否找到。
func Remove(list []config.SkillRepo, repo config.SkillRepo) ([]config.SkillRepo, bool) {
	found := false
	updated := list[:0]
	for _, r := range list {
		if r.Owner == repo.Owner && r.Name == repo.Name {
			found = true
			continue
		}
		updated = append(updated, r)
	}
	return updated, found
}

// Update 用 updated 整体替换 original（按 owner/name 定位）。
// 改名/改 owner 时校验不与列表内其他条目冲突。
func Update(list []config.SkillRepo, original, updated config.SkillRepo) ([]config.SkillRepo, error) {
	if original.Owner == "" || original.Name == "" {
		return list, fmt.Errorf("original repo owner and name required")
	}
	if updated.Owner == "" || updated.Name == "" {
		return list, fmt.Errorf("updated repo owner and name required")
	}
	normalize(&updated)
	origIdx := -1
	for i, r := range list {
		if r.Owner == original.Owner && r.Name == original.Name {
			origIdx = i
			break
		}
	}
	if origIdx == -1 {
		return list, fmt.Errorf("repo %s/%s not found", original.Owner, original.Name)
	}
	if !(updated.Owner == original.Owner && updated.Name == original.Name) {
		for i, r := range list {
			if i == origIdx {
				continue
			}
			if r.Owner == updated.Owner && r.Name == updated.Name {
				return list, fmt.Errorf("repo %s/%s already exists", updated.Owner, updated.Name)
			}
		}
	}
	out := make([]config.SkillRepo, len(list))
	copy(out, list)
	out[origIdx] = updated
	return out, nil
}
