package skills

import "os"
import "path/filepath"

type copyClass struct {
	kind      ConflictKind // "" = 无冲突
	localHash string
}

// classifyCopyEntry 判定 agent 目录中一个已存在条目与 SSOT 的关系。
// copy 模式下与 SSOT 全同的普通目录是正常投影（D5），不产冲突；
// symlink 模式下同状态是散装残留（plain_same）。
func classifyCopyEntry(method SyncMethod, ssotPath, target, ssotHash string, ssotComplete bool) copyClass {
	info, err := os.Lstat(target)
	if err != nil {
		return copyClass{}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(target); err != nil {
			return copyClass{kind: ConflictBrokenLink}
		}
		link, err := os.Readlink(target)
		if err != nil {
			return copyClass{kind: ConflictWrongTarget}
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(target), link)
		}
		a, aerr := filepath.Abs(link)
		b, berr := filepath.Abs(ssotPath)
		if aerr != nil || berr != nil || filepath.Clean(a) != filepath.Clean(b) {
			return copyClass{kind: ConflictWrongTarget}
		}
		return copyClass{}
	}
	localHash, complete := HashDir(target)
	if ssotComplete && complete && ssotHash != "" && localHash == ssotHash {
		if method == SyncMethodCopy {
			return copyClass{}
		}
		return copyClass{kind: ConflictPlainSame, localHash: localHash}
	}
	return copyClass{kind: ConflictPlainDiff, localHash: localHash}
}
