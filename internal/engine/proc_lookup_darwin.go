//go:build darwin

package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// lookupLoopbackClients 在 macOS 下通过一次批量 lsof 查询连向本地代理端口的真实客户端进程，
// 避免底层核心库按端口反查时误将本进程（FlyShadow Bar 服务端 socket）当作来源进程。
func lookupLoopbackClients(proxyPort int) map[string]string {
	if proxyPort <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "lsof", "-nP", "+c", "0", "-i", fmt.Sprintf(":%d", proxyPort), "-F", "pcn")
	out, _ := cmd.Output()
	if len(out) == 0 {
		return nil
	}
	return parseLsofClientMap(string(out), os.Getpid())
}
