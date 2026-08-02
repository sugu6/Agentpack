package main

import (
	"encoding/json"
	"fmt"

	"agentpack/internal/backup"
	"agentpack/internal/config"
)

func (a *App) ListBackups() ([]backup.Summary, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return []backup.Summary{}, nil
	}
	return backups.ListSummaries()
}

func (a *App) GetBackup(id string) (backup.Snapshot, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return backup.Snapshot{}, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return backup.Snapshot{}, fmt.Errorf("backup manager not initialized")
	}
	return backups.GetSnapshot(id)
}

func (a *App) DeleteBackup(id string) error {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return fmt.Errorf("backup manager not initialized")
	}
	return backups.Delete(id)
}

func (a *App) RestoreBackup(id string, opts backup.ImportOptions) (backup.ImportResult, error) {
	if err := a.assertInit(); err != nil {
		return backup.ImportResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return backup.ImportResult{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	var closed bool
	var exporter *backup.Exporter
	var backupsMgr *backup.Manager
	var mcpErr error

	func() {
		a.mu.RLock()
		closed = a.closed
		if !closed && opts.ApplyMCP {
			mcpErr = a.requireMcpStoreReadyLocked()
		}
		exporter = a.exporter
		backupsMgr = a.backups
		a.mu.RUnlock()
	}()

	if closed {
		return backup.ImportResult{}, fmt.Errorf("app is shutting down")
	}
	if mcpErr != nil {
		return backup.ImportResult{}, mcpErr
	}
	if exporter == nil {
		return backup.ImportResult{}, fmt.Errorf("exporter not initialized")
	}
	if backupsMgr == nil {
		return backup.ImportResult{}, fmt.Errorf("backup manager not initialized")
	}

	res, err := exporter.RestoreFromBackup(backupsMgr, id, opts)
	if err != nil {
		return res, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return res, nil
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return res, nil
}

func (a *App) ExportBackupToFile(id, dest string) (string, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return "", fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return "", fmt.Errorf("backup manager not initialized")
	}
	return backups.ExportToFile(id, dest)
}

func (a *App) ImportBackupFromFile(src string, opts backup.ImportOptions) (backup.ImportResult, error) {
	if err := a.assertInit(); err != nil {
		return backup.ImportResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return backup.ImportResult{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	var closed bool
	var exporter *backup.Exporter
	var mcpErr error

	func() {
		a.mu.RLock()
		closed = a.closed
		if !closed && opts.ApplyMCP {
			mcpErr = a.requireMcpStoreReadyLocked()
		}
		exporter = a.exporter
		a.mu.RUnlock()
	}()

	if closed {
		return backup.ImportResult{}, fmt.Errorf("app is shutting down")
	}
	if mcpErr != nil {
		return backup.ImportResult{}, mcpErr
	}
	if exporter == nil {
		return backup.ImportResult{}, fmt.Errorf("exporter not initialized")
	}

	res, err := exporter.ImportFromFile(src, opts)
	if err != nil {
		return res, err
	}

	var importedSettings *config.Settings
	if opts.ApplySettings && len(res.ExportedSettings) > 0 {
		data, marshalErr := json.Marshal(res.ExportedSettings)
		if marshalErr != nil {
			return res, fmt.Errorf("import: encode settings: %w", marshalErr)
		}
		var settings config.Settings
		if unmarshalErr := json.Unmarshal(data, &settings); unmarshalErr != nil {
			return res, fmt.Errorf("import: decode settings: %w", unmarshalErr)
		}
		if settings.BackupRetention <= 0 {
			if settings.BackupCount > 0 {
				settings.BackupRetention = settings.BackupCount
			} else {
				settings.BackupRetention = config.DefaultSettings().BackupRetention
			}
		}
		if settings.BackupCount <= 0 {
			settings.BackupCount = settings.BackupRetention
		}
		importedSettings = &settings
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return res, nil
	}
	if a.cfg == nil {
		a.cfg = config.Default()
	}
	if opts.ApplyAgentStatus && a.registry != nil {
		a.cfg.DisabledAgents = a.registry.DisabledIDs()
	}
	cfgAfterAgentStatus := *a.cfg
	a.mu.Unlock()

	// Apply imported settings through the normal runtime-aware path. The
	// current function already owns storeOpMu, so release it before calling
	// UpdateSettings, which acquires the same lock.
	if importedSettings != nil {
		a.storeOpMu.Unlock()
		settingsErr := a.UpdateSettings(*importedSettings)
		a.storeOpMu.Lock()
		if settingsErr != nil {
			return res, fmt.Errorf("import: apply settings: %w", settingsErr)
		}
	} else if opts.ApplyAgentStatus {
		if err := config.Save(&cfgAfterAgentStatus); err != nil {
			return res, fmt.Errorf("import: save agent status: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.emitAgentsChangedLocked()
	if a.mcpStore != nil {
		a.emitLocked("mcp:changed", a.mcpStore.List())
	}
	return res, nil
}

func (a *App) CreateBackupNow(description string) (backup.Summary, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return backup.Summary{}, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return backup.Summary{}, fmt.Errorf("backup manager not initialized")
	}
	return backups.Capture("manual", "", "", description)
}
