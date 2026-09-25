package main

import (
	"testing"
)

func TestParseAppVersion_Valid(t *testing.T) {
	data := []byte(`version: '3'

info:
  companyName: "sugu6"
  version: "0.2.4"
`)
	if got := parseVersionFromYAML(data); got != "0.2.4" {
		t.Fatalf("expected 0.2.4, got %s", got)
	}
}

func TestParseAppVersion_TopLevelVersionIgnored(t *testing.T) {
	// 顶层 version: '3' 不应被误匹配
	data := []byte(`version: '3'

info:
  version: "1.2.3"
`)
	if got := parseVersionFromYAML(data); got != "1.2.3" {
		t.Fatalf("expected 1.2.3, got %s", got)
	}
}

func TestParseAppVersion_NoInfoBlock(t *testing.T) {
	data := []byte(`version: '3'
dev_mode:
  log_level: warn
`)
	if got := parseVersionFromYAML(data); got != "0.0.0" {
		t.Fatalf("expected 0.0.0, got %s", got)
	}
}

func TestParseAppVersion_InfoWithoutVersion(t *testing.T) {
	data := []byte(`info:
  companyName: "test"
`)
	if got := parseVersionFromYAML(data); got != "0.0.0" {
		t.Fatalf("expected 0.0.0, got %s", got)
	}
}

func TestParseAppVersion_NestedVersionIgnored(t *testing.T) {
	// info 块外的 version 不应被匹配
	data := []byte(`info:
  companyName: "test"
other:
  version: "9.9.9"
`)
	if got := parseVersionFromYAML(data); got != "0.0.0" {
		t.Fatalf("expected 0.0.0, got %s", got)
	}
}

func TestParseAppVersion_TrailingCommentWithQuotes(t *testing.T) {
	// 带尾引号的版本值 + 行内注释：截断必须先于去引号，
	// 否则会得到带尾引号的 "0.3.0\""
	data := []byte(`info:
  version: "0.3.0"  # x
`)
	if got := parseVersionFromYAML(data); got != "0.3.0" {
		t.Fatalf("expected 0.3.0, got %q", got)
	}
}

func TestParseAppVersion_InlineCommentNoSpace(t *testing.T) {
	// # 前无空白的行内注释也要截断
	data := []byte(`info:
  version: 0.3.0#build
`)
	if got := parseVersionFromYAML(data); got != "0.3.0" {
		t.Fatalf("expected 0.3.0, got %q", got)
	}
}

func TestParseAppVersion_MultipleSpacesAfterColon(t *testing.T) {
	// 冒号后多个空格：TrimSpace 必须在按空白截断之前完成，
	// 否则首个空白在索引 0，截断出空版本号
	data := []byte(`info:
  version:   0.3.0
`)
	if got := parseVersionFromYAML(data); got != "0.3.0" {
		t.Fatalf("expected 0.3.0, got %q", got)
	}
}

func TestParseAppVersion_QuotedVersionWithTrailingComment(t *testing.T) {
	// 带引号值 + 行内注释：注释截断必须先于空白截断，
	// 否则 `0.3.0" # x` 的尾引号会随注释残留
	data := []byte(`info:
  version: "0.3.0" # x
`)
	if got := parseVersionFromYAML(data); got != "0.3.0" {
		t.Fatalf("expected 0.3.0, got %q", got)
	}
}
