package market

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mockSkillsSh(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := skillsShAPIBase
	skillsShAPIBase = srv.URL
	t.Cleanup(func() { skillsShAPIBase = orig })
}

func searchResponse(items string) string {
	return fmt.Sprintf(`{"query":"q","searchType":"all","count":0,"duration_ms":1,"skills":[%s]}`, items)
}

func skillJSON(id, skillID, name, source string, installs int64) string {
	return fmt.Sprintf(`{"id":%q,"skillId":%q,"name":%q,"installs":%d,"source":%q}`,
		id, skillID, name, installs, source)
}

func TestBackfillSkillSources_ExactMatchPicksHighestInstalls(t *testing.T) {
	mockSkillsSh(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "code-simplifier" {
			t.Errorf("unexpected query %q", r.URL.Query().Get("q"))
		}
		_, _ = w.Write([]byte(searchResponse(
			skillJSON("a/repo/code-simplifier", "code-simplifier", "code-simplifier", "a/repo", 100) + "," +
				skillJSON("b/repo/code-simplifier", "code-simplifier", "code-simplifier", "b/repo", 900))))
	})
	matches, err := BackfillSkillSources(context.Background(), []string{"code-simplifier"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := matches["code-simplifier"]
	if !ok {
		t.Fatal("expected match for code-simplifier")
	}
	if m.Owner != "b" || m.Repo != "repo" || m.Installs != 900 {
		t.Fatalf("expected highest-installs match b/repo, got %+v", m)
	}
}

func TestBackfillSkillSources_RejectsSubstringMatch(t *testing.T) {
	mockSkillsSh(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchResponse(
			skillJSON("mansion/argent/argent-metro-debugger", "argent-metro-debugger", "argent-metro-debugger", "software-mansion/argent", 9999) + "," +
				skillJSON("llm/debugger", "debugger", "debugger", "shubhamsaboo/awesome-llm-apps", 500))))
	})
	matches, err := BackfillSkillSources(context.Background(), []string{"debugger"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := matches["debugger"]
	if !ok {
		t.Fatal("expected exact match for debugger")
	}
	if m.Owner != "shubhamsaboo" || m.Repo != "awesome-llm-apps" {
		t.Fatalf("expected exact skillId match, got %+v", m)
	}
}

func TestBackfillSkillSources_NoExactMatchSkipped(t *testing.T) {
	mockSkillsSh(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchResponse(
			skillJSON("x/y/shared-utils", "shared-utils", "shared-utils", "x/y", 100))))
	})
	matches, err := BackfillSkillSources(context.Background(), []string{"shared"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := matches["shared"]; ok {
		t.Fatal("expected no match for non-exact directory")
	}
}

func TestBackfillSkillSources_EmptyDirectories(t *testing.T) {
	matches, err := BackfillSkillSources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %v", matches)
	}
}
