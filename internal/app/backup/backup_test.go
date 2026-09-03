package backup

import (
	"testing"

	"agentpack/internal/backup"
	"agentpack/internal/config"
)

func TestNormalizeConfig(t *testing.T) {
	cases := []struct {
		name          string
		in            config.Settings
		wantRetention int
		wantCount     int
	}{
		{"defaults", config.Settings{}, 0, 0},
		{"retention negative uses count", config.Settings{BackupRetention: -1, BackupCount: 7}, 7, 7},
		{"retention negative no count uses default", config.Settings{BackupRetention: -1}, config.DefaultSettings().BackupRetention, config.DefaultSettings().BackupRetention},
		{"count zero uses retention", config.Settings{BackupRetention: 5}, 5, 5},
		{"retention zero keeps count", config.Settings{BackupRetention: 0, BackupCount: 3}, 0, 3},
	}
	for _, c := range cases {
		got := NormalizeConfig(c.in)
		if got.BackupRetention != c.wantRetention || got.BackupCount != c.wantCount {
			t.Errorf("%s: NormalizeConfig = (retention=%d, count=%d), want (%d, %d)",
				c.name, got.BackupRetention, got.BackupCount, c.wantRetention, c.wantCount)
		}
	}
}

func TestExtractSettingsFromSnapshot(t *testing.T) {
	// nil Settings → nil
	if got, _ := ExtractSettingsFromSnapshot(backup.Snapshot{}); got != nil {
		t.Error("expected nil for snapshot without settings")
	}
	// 无 appSettings 键 → nil
	if got, _ := ExtractSettingsFromSnapshot(backup.Snapshot{Settings: map[string]any{"other": 1}}); got != nil {
		t.Error("expected nil when appSettings missing")
	}
	// 有效 appSettings → 解析
	snap := backup.Snapshot{Settings: map[string]any{
		"appSettings": map[string]any{"autoBackup": true, "backupCount": float64(3)},
	}}
	got, err := ExtractSettingsFromSnapshot(snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || !got.AutoBackup || got.BackupCount != 3 {
		t.Fatalf("unexpected settings: %+v", got)
	}
}
