package main

import (
	"fmt"

	"agentpack/internal/market"
	"agentpack/internal/mcp"
)

func (a *App) ListMcpServers() ([]mcp.Server, error) {
	_, ms, _, _, _, _ := a.snapshot()
	if ms == nil {
		return []mcp.Server{}, nil
	}
	return ms.List(), nil
}

func (a *App) ScanMcpServers() (*mcp.ScanResult, error) {
	reg, ms, _, _, _, _ := a.snapshot()
	if reg == nil {
		return nil, fmt.Errorf("registry not initialized")
	}
	if ms == nil {
		return nil, fmt.Errorf("mcp store not initialized")
	}
	return ms.Scan(reg), nil
}

func (a *App) GetMcpServer(id string) (mcp.Server, error) {
	_, ms, _, _, _, _ := a.snapshot()
	if ms == nil {
		return mcp.Server{}, fmt.Errorf("store not initialized")
	}
	srv, ok := ms.Get(id)
	if !ok {
		return mcp.Server{}, fmt.Errorf("server %s not found", id)
	}
	return srv, nil
}

func (a *App) GetAgentMcpServers(agentID string) ([]mcp.Server, error) {
	_, ms, _, _, _, _ := a.snapshot()
	if ms == nil {
		return []mcp.Server{}, nil
	}
	return ms.ByAgent(agentID), nil
}

func (a *App) AddMcpServer(server mcp.Server, agentIDs []string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if _, err := a.mcpStore.Add(server, agentIDs, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) UpdateMcpServer(id string, server mcp.Server, agentIDs []string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if err := a.mcpStore.Update(id, server, agentIDs, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) DeleteMcpServer(id string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if err := a.mcpStore.Remove(id, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) ToggleMcpServerAgent(id, agentID string, enabled bool) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if err := a.mcpStore.ToggleAgent(id, agentID, enabled, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) InstallMarketServer(server market.MarketServer, agentIDs []string) (mcp.Server, error) {
	if err := a.assertInit(); err != nil {
		return mcp.Server{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return mcp.Server{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	if server.Name == "" {
		return mcp.Server{}, fmt.Errorf("server name required")
	}
	if server.Command == "" && server.URL == "" {
		return mcp.Server{}, fmt.Errorf("server must have command or url")
	}
	env := server.Env
	if env == nil {
		env = map[string]string{}
	}
	transport := server.Transport
	if transport == "" {
		transport = "stdio"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return mcp.Server{}, fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return mcp.Server{}, err
	}
	created, err := a.mcpStore.Add(mcp.Server{
		Name:        server.Name,
		Description: server.Description,
		Command:     server.Command,
		Args:        server.Args,
		Env:         env,
		Transport:   mcp.Transport(transport),
		URL:         server.URL,
		Source:      string(server.Source),
		SourceID:    server.SourceID,
	}, agentIDs, a.registry)
	if err != nil {
		return mcp.Server{}, err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return created, nil
}

// SearchMarketSkills 搜索市场中的 Skills，合并所有来源并按下载量排序
// SearchMarketSkills 搜索市场 skills
// source 参数："" 表示搜索全部启用的来源，"github" 仅 GitHub 仓库，"skills-sh" 仅 skills.sh
// page 从 1 开始，支持分页（无限滚动）
