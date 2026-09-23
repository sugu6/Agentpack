package skills

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Matches 仅当两侧指纹与当前完全一致时成立。
func (a ConflictAck) Matches(ssotHash, localHash string) bool {
	return a.SSOTHash != "" && a.SSOTHash == ssotHash && a.LocalHash == localHash
}

func conflictAckPath(ssotDir string) string {
	return filepath.Join(filepath.Dir(ssotDir), ".skill-conflict-ack.json")
}

func conflictKey(directory, target string) string {
	return directory + "|" + filepath.Clean(target)
}

type conflictAckFile struct {
	Version int                    `json:"version"`
	Acks    map[string]ConflictAck `json:"acks"`
}

// ReadConflictAcks 读取保留记录；文件缺失或损坏返回空表，扫描不因此失败。
func ReadConflictAcks(ssotDir string) map[string]ConflictAck {
	data, err := os.ReadFile(conflictAckPath(ssotDir))
	if err != nil {
		return map[string]ConflictAck{}
	}
	var f conflictAckFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("warn: corrupt conflict ack file ignored: %v", err)
		return map[string]ConflictAck{}
	}
	if f.Acks == nil {
		return map[string]ConflictAck{}
	}
	return f.Acks
}

func writeConflictAcks(ssotDir string, acks map[string]ConflictAck) error {
	data, err := json.MarshalIndent(conflictAckFile{Version: 1, Acks: acks}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(conflictAckPath(ssotDir), data, 0644)
}

// WriteConflictAck 记录（或覆盖）一条保留决定。
func WriteConflictAck(ssotDir, key string, ack ConflictAck) error {
	acks := ReadConflictAcks(ssotDir)
	acks[key] = ack
	return writeConflictAcks(ssotDir, acks)
}

// DeleteConflictAck 删除一条保留决定（收编/覆盖后调用）。
func DeleteConflictAck(ssotDir, key string) error {
	acks := ReadConflictAcks(ssotDir)
	if _, ok := acks[key]; !ok {
		return nil
	}
	delete(acks, key)
	return writeConflictAcks(ssotDir, acks)
}
