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

func TestBackfillSkillSources_ReturnsCandidatesByInstallsDesc(t *testing.T) {
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
	cands, ok := matches["code-simplifier"]
	if !ok || len(cands) != 2 {
		t.Fatalf("expected 2 candidates for code-simplifier, got %v", cands)
	}
	if cands[0].Owner != "b" || cands[0].Repo != "repo" || cands[0].Installs != 900 {
		t.Fatalf("expected highest-installs candidate first, got %+v", cands[0])
	}
	if cands[1].Installs != 100 {
		t.Fatalf("expected lower-installs candidate second, got %+v", cands[1])
	}
}

func TestBackfillSkillSources_IncludesAllGithubCandidates(t *testing.T) {
	mockSkillsSh(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchResponse(
			skillJSON("mansion/argent/argent-metro-debugger", "argent-metro-debugger", "argent-metro-debugger", "software-mansion/argent", 9999) + "," +
				skillJSON("llm/debugger", "debugger", "debugger", "shubhamsaboo/awesome-llm-apps", 500))))
	})
	matches, err := BackfillSkillSources(context.Background(), []string{"debugger"})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := matches["debugger"]
	if !ok || len(cands) != 2 {
		t.Fatalf("expected both candidates (content decides), got %v", cands)
	}
	if cands[0].Owner != "software-mansion" || cands[0].Installs != 9999 {
		t.Fatalf("expected argent candidate first by installs, got %+v", cands[0])
	}
}

func TestBackfillSkillSources_NonGithubSourceFiltered(t *testing.T) {
	mockSkillsSh(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchResponse(
			skillJSON("smithery/shared", "shared", "shared", "smithery.ai", 9999) + "," +
				skillJSON("x/y/shared", "shared", "shared", "x/y", 100))))
	})
	matches, err := BackfillSkillSources(context.Background(), []string{"shared"})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := matches["shared"]
	if !ok || len(cands) != 1 {
		t.Fatalf("expected only github candidate (domain filtered), got %v", cands)
	}
	if cands[0].Owner != "x" || cands[0].Repo != "y" {
		t.Fatalf("expected x/y candidate, got %+v", cands[0])
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
