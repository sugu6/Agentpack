package mcp

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/database"
)

// seedManagedFromDisk 把磁盘上各 agent 配置文件中的条目写入数据库基线，
// 模拟"上一会话已受管"状态（旧版本 Load 全量镜像的结果，也是升级后
// 既有数据的真实形态）。新 Load 语义下，只有出现在基线中的服务器才会被
// 加载进 store；因此断言 Load 后 store 内容的测试必须先调用本函数，
// 为待加载的服务器建立受管基线。
func seedManagedFromDisk(t *testing.T, reg *agents.Registry) {
	t.Helper()
	db := database.GetDB()
	if db == nil {
		t.Fatal("database not initialized")
	}
	now := time.Now().Unix()
	for _, ag := range reg.All() {
		if ag.ConfigPath == "" || ag.Status == agents.StatusNotFound {
			continue
		}
		servers, err := NewBackend(string(ag.Type)).Read(ag.ConfigPath)
		if err != nil && !errors.Is(err, ErrPartialRead) {
			continue // 非法配置：Load 阶段同样跳过该文件
		}
		for name, srv := range servers {
			id := srv.ID
			if id == "" {
				id = name + "@" + ag.ConfigPath
			}
			argsJSON, err := json.Marshal(srv.Args)
			if err != nil {
				t.Fatalf("seed marshal args for %s: %v", id, err)
			}
			_, err = db.Exec(`INSERT OR REPLACE INTO mcp_servers
				(id, name, description, command, args, env, transport, config_type, url, timeout, source, source_id, installed_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, srv.Name, srv.Description, srv.Command, string(argsJSON), "", string(srv.Transport), srv.ConfigType, srv.URL, srv.Timeout, srv.Source, srv.SourceID, now, now)
			if err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
	}
}
