package skillrepos

import (
	"testing"

	"agentpack/internal/config"
)

func TestAdd(t *testing.T) {
	list, err := Add(nil, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"})
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(list))
	}
	// 空分支默认 main
	list, err = Add(list, config.SkillRepo{Owner: "ComposioHQ", Name: "awesome-claude-skills"})
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if list[1].Branch != "main" {
		t.Errorf("expected Branch=main (default), got %q", list[1].Branch)
	}
	// 重复报错
	if _, err := Add(list, config.SkillRepo{Owner: "anthropics", Name: "skills"}); err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	// 缺 owner/name 报错
	if _, err := Add(list, config.SkillRepo{Name: "skills"}); err == nil {
		t.Fatal("expected owner-required error, got nil")
	}
}

func TestRemove(t *testing.T) {
	list := []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
		{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: "main"},
	}
	updated, found := Remove(list, config.SkillRepo{Owner: "anthropics", Name: "skills"})
	if !found {
		t.Fatal("expected found=true")
	}
	if len(updated) != 1 || updated[0].Name != "awesome-claude-skills" {
		t.Fatalf("unexpected updated list: %+v", updated)
	}
	if _, found := Remove(list, config.SkillRepo{Owner: "missing", Name: "repo"}); found {
		t.Fatal("expected found=false for missing repo")
	}
}

func TestUpdate_ChangeBranchOnly(t *testing.T) {
	list := []config.SkillRepo{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	out, err := Update(list, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "dev"})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if out[0].Branch != "dev" {
		t.Errorf("expected Branch=dev, got %q", out[0].Branch)
	}
	// 原列表不被修改（返回新切片）
	if list[0].Branch != "main" {
		t.Errorf("original list mutated: %+v", list[0])
	}
}

func TestUpdate_ChangeOwnerName(t *testing.T) {
	list := []config.SkillRepo{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	out, err := Update(list, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}, config.SkillRepo{Owner: "myfork", Name: "skills", Branch: "main"})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if out[0].Owner != "myfork" || out[0].Name != "skills" {
		t.Errorf("expected myfork/skills, got %s/%s", out[0].Owner, out[0].Name)
	}
}

func TestUpdate_DuplicateReturnsError(t *testing.T) {
	list := []config.SkillRepo{
		{Owner: "anthropics", Name: "skills", Branch: "main"},
		{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: "main"},
	}
	_, err := Update(list, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}, config.SkillRepo{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: "dev"})
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
}

func TestUpdate_NotFound(t *testing.T) {
	list := []config.SkillRepo{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	_, err := Update(list, config.SkillRepo{Owner: "nonexistent", Name: "repo", Branch: "main"}, config.SkillRepo{Owner: "myfork", Name: "skills", Branch: "main"})
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

func TestUpdate_EmptyBranchDefaultsToMain(t *testing.T) {
	list := []config.SkillRepo{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	out, err := Update(list, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: ""})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if out[0].Branch != "main" {
		t.Errorf("expected Branch=main (default), got %q", out[0].Branch)
	}
}

func TestUpdate_EmptyOriginalReturnsError(t *testing.T) {
	_, err := Update(nil, config.SkillRepo{Owner: "", Name: "skills"}, config.SkillRepo{Owner: "anthropics", Name: "skills"})
	if err == nil {
		t.Fatal("expected error for empty original owner, got nil")
	}
}

func TestUpdate_EmptyUpdatedReturnsError(t *testing.T) {
	list := []config.SkillRepo{{Owner: "anthropics", Name: "skills", Branch: "main"}}
	_, err := Update(list, config.SkillRepo{Owner: "anthropics", Name: "skills", Branch: "main"}, config.SkillRepo{Owner: "", Name: "skills"})
	if err == nil {
		t.Fatal("expected error for empty updated owner, got nil")
	}
}
