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
