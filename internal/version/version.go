package version

import "fmt"

// 构建时可通过 -ldflags 注入以下版本元信息。
var (
	Version    = "0.1.0-dev"
	Commit     = "none"
	BuildDate  = "unknown"
	GithubRepo = "KevinXC5/flyshadow-bar"
)

// Full 返回带提交哈希与构建时间的可读版本字符串。
func Full() string {
	if Commit == "" || Commit == "none" {
		return Version
	}
	short := Commit
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s (%s, %s)", Version, short, BuildDate)
}
