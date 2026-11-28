package ui

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/capture"
	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

// captureViewModel 持有独立宽屏“抓包分析”大窗口的 UI 状态（仅在主线程或测试线程读写）。
type captureViewModel struct {
	app *App

	status      engine.CaptureStatus
	entries     []capture.Entry
	selectedID          uint64
	searchQuery         string
	protoFilter         int // 0=全部, 1=HTTP(S), 2=TCP, 3=UDP
	detailTab           int // 0=概览, 1=Query, 2=请求, 3=响应
	reqHeadersCollapsed bool
	respHeadersCollapsed bool
	udpExpanded         map[int]bool

	// 证书与白名单弹窗状态
	certModalOpen  bool
	certBusy       bool
	certActionErr  string
	mitmHostDraft  string
	togglingProxy  bool
	toastMsg       string
	toastExpiresAt time.Time
}

func newCaptureViewModel(app *App) *captureViewModel {
	vm := &captureViewModel{
		app:         app,
		udpExpanded: make(map[int]bool),
	}
	if app != nil && app.eng != nil {
		vm.status = app.eng.CaptureStatus()
		vm.entries = app.eng.CaptureStore().Snapshot()
	}
	return vm
}

func (vm *captureViewModel) showToast(msg string) {
	if msg == "" {
		return
	}
	vm.toastMsg = msg
	vm.toastExpiresAt = time.Now().Add(toastPendingTTL)
}

func (vm *captureViewModel) runAsync(fn func() error, okToast string) {
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	go func() {
		err := fn()
		st := vm.app.eng.CaptureStatus()
		entries := vm.app.eng.CaptureStore().Snapshot()
		vm.app.updateCaptureWindow(func() {
			vm.status = st
			vm.entries = entries
			if err != nil {
				vm.showToast(err.Error())
			} else if okToast != "" {
				vm.showToast(okToast)
			}
		})
	}()
}

func (vm *captureViewModel) render(c *ui.Context) {
	if vm.app != nil {
		if c.Shortcut(ui.Cmd, ui.KeyW) {
			if vm.certModalOpen {
				vm.certModalOpen = false
			} else {
				vm.app.hideCaptureWindow()
			}
		} else if c.Shortcut(0, ui.KeyEscape) && vm.certModalOpen {
			vm.certModalOpen = false
		}
	}
	if vm.toastMsg != "" {
		msg := vm.toastMsg
		exp := vm.toastExpiresAt
		vm.toastMsg = ""
		vm.toastExpiresAt = time.Time{}
		if exp.IsZero() || time.Now().Before(exp) {
			c.Toast(msg)
		}
	}

	p := paletteFor(c.Theme().Dark)

	ui.Column(c).Fill().Background(p.canvas).Children(func() {
		vm.renderHeaderBar(c, p)
		vm.renderFilterBar(c, p)

		// 主工作区：左侧请求列表（约 44%）+ 右侧详情面板（约 56%）
		ui.Row(c).Grow(1).MinHeight(0).Padding(8, 12, 12, 12).Gap(10).AlignItems(ui.Stretch).Children(func() {
			vm.renderEntryListPane(c, p)
			vm.renderDetailPane(c, p)
		})
	})

	vm.renderCertModal(c, p)
}

func (vm *captureViewModel) renderHeaderBar(c *ui.Context, p palette) {
	st := vm.status

	ui.Column(c).Background(p.card).Children(func() {
		ui.Row(c).Padding(10, 14).AlignItems(ui.Center).Gap(10).Children(func() {
		// 左侧应用标识与标题
		ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
			ui.Box(c).Size(24, 24).Radius(7).Background(p.accentSoft).Center().Children(func() {
				ui.Icon(c, svgActivity).Size(14, 14).TextColor(p.accent)
			})
			ui.Text(c, "抓包分析").FontSize(14).Bold().TextColor(p.text).NoWrap()
			if st.Enabled {
				statusText := fmt.Sprintf("录制中 · :%d", st.ProxyPort)
				fg, bg := p.danger, p.dangerSoft
				if st.Paused {
					statusText = fmt.Sprintf("已暂停 · :%d", st.ProxyPort)
					fg, bg = p.warning, p.warningSoft
				}
				renderPill(c, statusText, fg, bg)
			} else {
				renderPill(c, "未开启（零开销直连模式）", p.textMuted, p.pillBg)
			}
		})

		ui.Spacer(c)

		// 录制控制按钮组
		if !st.Enabled {
			startBtn := ui.Row(c.Key("cap-start")).Padding(5, 11).Radius(7.5).
				Background(p.accent).AlignItems(ui.Center).Gap(5).Label("开启抓包")
			if vm.togglingProxy {
				startBtn.Opacity(0.6)
			} else {
				startBtn.Cursor(ui.CursorPointer)
				if startBtn.Hovered() {
					startBtn.Opacity(0.92)
				}
			}
			startBtn.Children(func() {
				if vm.togglingProxy {
					ui.Spinner(c).Size(11, 11)
				} else {
					ui.Icon(c, svgPlay).Size(11, 11).TextColor(ui.Hex("#ffffff"))
				}
				ui.Text(c, "开启抓包").FontSize(11.5).Bold().TextColor(ui.Hex("#ffffff")).NoWrap()
			})
			if !vm.togglingProxy && startBtn.Clicked() {
				vm.togglingProxy = true
				vm.toggleCaptureProxy(true)
			}
		} else {
			// 暂停/继续
			pauseLabel := "暂停"
			pauseIcon := svgPause
			if st.Paused {
				pauseLabel = "继续"
				pauseIcon = svgPlay
			}
			pauseBtn := ui.Row(c.Key("cap-pause")).Padding(5, 9).Radius(7.5).
				Background(p.cardElevated).Border(1, p.border).AlignItems(ui.Center).Gap(4.5).
				Cursor(ui.CursorPointer).Label(pauseLabel)
			if pauseBtn.Hovered() {
				pauseBtn.Background(p.cardHover)
			}
			pauseBtn.Children(func() {
				ui.Icon(c, pauseIcon).Size(11.5, 11.5).TextColor(p.textSecondary)
				ui.Text(c, pauseLabel).FontSize(11.5).TextColor(p.textSecondary).NoWrap()
			})
			if pauseBtn.Clicked() && vm.app != nil && vm.app.eng != nil {
				vm.app.eng.SetCapturePaused(!st.Paused)
				vm.status = vm.app.eng.CaptureStatus()
			}

			// 停止抓包（还原直连零开销）
			stopBtn := ui.Row(c.Key("cap-stop")).Padding(5, 10).Radius(7.5).
				Background(p.dangerSoft).Border(1, p.danger).AlignItems(ui.Center).Gap(4.5).Label("停止抓包")
			if vm.togglingProxy {
				stopBtn.Opacity(0.6)
			} else {
				stopBtn.Cursor(ui.CursorPointer)
			}
			stopBtn.Children(func() {
				if vm.togglingProxy {
					ui.Spinner(c).Size(11, 11)
				} else {
					ui.Box(c).Size(7, 7).Radius(1.5).Background(p.danger)
				}
				ui.Text(c, "停止抓包").FontSize(11.5).Bold().TextColor(p.danger).NoWrap()
			})
			if !vm.togglingProxy && stopBtn.Clicked() {
				vm.togglingProxy = true
				vm.toggleCaptureProxy(false)
			}
		}

		// 清空记录
		clearBtn := ui.Row(c.Key("cap-clear")).Padding(5, 9).Radius(7.5).
			Background(p.cardElevated).Border(1, p.border).AlignItems(ui.Center).Gap(4.5).
			Cursor(ui.CursorPointer).Label("清空").Tooltip("清空内存中的所有抓包记录")
		if clearBtn.Hovered() {
			clearBtn.Background(p.cardHover)
		}
		clearBtn.Children(func() {
			ui.Icon(c, svgTrash).Size(12, 12).TextColor(p.textSecondary)
			ui.Text(c, "清空").FontSize(11.5).TextColor(p.textSecondary).NoWrap()
		})
		if clearBtn.Clicked() && vm.app != nil && vm.app.eng != nil {
			vm.app.eng.ClearCapture()
			vm.entries = nil
			vm.selectedID = 0
			vm.status = vm.app.eng.CaptureStatus()
		}

		// HTTPS 白名单解密与证书设置入口按钮
		certText := "HTTPS 解密与证书"
		certFg, certBg, certBorder := p.textSecondary, p.cardElevated, p.border
		if st.MITMEnabled {
			if st.Cert.Trusted {
				certText = fmt.Sprintf("HTTPS 解密 · %d 域名", len(st.MITMHosts))
				certFg, certBg, certBorder = p.success, p.successSoft, p.success
			} else {
				certText = "CA 未信任（点击安装）"
				certFg, certBg, certBorder = p.warning, p.warningSoft, p.warning
			}
		} else if st.Cert.Trusted {
			certText = "HTTPS 解密：关（CA 已就绪）"
		}
		certBtn := ui.Row(c.Key("cap-cert-btn")).Padding(5, 10).Radius(7.5).
			Background(certBg).Border(1, certBorder).AlignItems(ui.Center).Gap(5).
			Cursor(ui.CursorPointer).Label("HTTPS 解密与证书设置").Tooltip("管理自签名根证书与白名单 HTTPS 解密域名")
		if certBtn.Hovered() && !st.MITMEnabled {
			certBtn.Background(p.cardHover)
		}
		certBtn.Children(func() {
			ui.Icon(c, svgShield).Size(12.5, 12.5).TextColor(certFg)
			ui.Text(c, certText).FontSize(11.5).Bold().TextColor(certFg).NoWrap()
		})
		if certBtn.Clicked() {
			vm.certModalOpen = true
			vm.certActionErr = ""
			if vm.app != nil && vm.app.eng != nil {
				go func() {
					st := vm.app.eng.CaptureStatus()
					cert := vm.app.eng.RefreshMITMCertStatus()
					st.Cert = cert
					vm.app.updateCaptureWindow(func() { vm.status = st })
				}()
			}
		}
		})
		ui.Box(c).Height(1).Background(p.border)
	})
}

func (vm *captureViewModel) toggleCaptureProxy(enable bool) {
	if vm.app == nil || vm.app.eng == nil {
		vm.togglingProxy = false
		return
	}
	go func() {
		err := vm.app.eng.SetCaptureEnabled(enable)
		st := vm.app.eng.CaptureStatus()
		entries := vm.app.eng.CaptureStore().Snapshot()
		vm.app.updateCaptureWindow(func() {
			vm.togglingProxy = false
			vm.status = st
			vm.entries = entries
			if err != nil {
				vm.showToast(err.Error())
			} else if enable {
				vm.showToast(fmt.Sprintf("抓包已开启，正在监听 :%d", st.ProxyPort))
			} else {
				vm.showToast("抓包已停止，已还原直连代理")
			}
		})
	}()
}

func (vm *captureViewModel) renderFilterBar(c *ui.Context, p palette) {
	ui.Row(c).Padding(8, 12, 0, 12).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.SearchField(c.Key("cap-search"), &vm.searchQuery).Grow(1).
			Placeholder("筛选域名、URL、进程、状态码、方法或 DNS 记录…")
		renderSegmented(c.Key("cap-proto-seg"), p, &vm.protoFilter, "全部", "HTTP(S)", "TCP", "UDP")
	})
}

func (vm *captureViewModel) filteredEntries() []capture.Entry {
	q := strings.ToLower(strings.TrimSpace(vm.searchQuery))
	out := make([]capture.Entry, 0, len(vm.entries))
	for _, e := range vm.entries {
		switch vm.protoFilter {
		case 1:
			if e.Kind != capture.KindHTTP && e.Kind != capture.KindHTTPS {
				continue
			}
		case 2:
			if e.Kind != capture.KindTCP {
				continue
			}
		case 3:
			if e.Kind != capture.KindUDP {
				continue
			}
		}
		if q != "" && !captureEntryMatches(e, q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func captureEntryMatches(e capture.Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.Host), q) ||
		strings.Contains(strings.ToLower(e.URL), q) ||
		strings.Contains(strings.ToLower(e.Path), q) ||
		strings.Contains(strings.ToLower(e.ProcessName), q) ||
		strings.Contains(strings.ToLower(e.Protocol), q) ||
		strings.Contains(strings.ToLower(e.Method), q) ||
		strings.Contains(strings.ToLower(e.TLSSNI), q) {
		return true
	}
	if e.StatusCode > 0 && strings.Contains(strconv.Itoa(e.StatusCode), q) {
		return true
	}
	for _, d := range e.DNSRecords {
		if strings.Contains(strings.ToLower(d.Name), q) {
			return true
		}
		for _, ans := range d.Answers {
			if strings.Contains(strings.ToLower(ans), q) {
				return true
			}
		}
	}
	if e.QUICInfo != nil && strings.Contains(strings.ToLower(e.QUICInfo.SNI), q) {
		return true
	}
	return false
}

func (vm *captureViewModel) renderEntryListPane(c *ui.Context, p palette) {
	filtered := vm.filteredEntries()
	httpCnt, udpCnt := 0, 0
	for _, e := range vm.entries {
		switch e.Kind {
		case capture.KindHTTP, capture.KindHTTPS:
			httpCnt++
		case capture.KindUDP:
			udpCnt++
		}
	}

	ui.Column(c).WidthPercent(36).MinWidth(320).Gap(6).Radius(12).
		Background(p.card).Border(1, p.border).Padding(10).Children(func() {
		// 顶部列表计数摘要
		ui.Row(c).AlignItems(ui.Center).Padding(0, 2).Children(func() {
			summary := fmt.Sprintf("共 %d 条 · HTTP(S) %d · UDP %d", len(vm.entries), httpCnt, udpCnt)
			if len(filtered) != len(vm.entries) {
				summary = fmt.Sprintf("匹配 %d / %d 条", len(filtered), len(vm.entries))
			}
			ui.Text(c, summary).FontSize(11).TextColor(p.textMuted).NoWrap()
			ui.Spacer(c)
			ui.Text(c, "仅存内存 · 最多 500 条").FontSize(10).TextColor(p.textMuted).NoWrap()
		})

		if len(filtered) == 0 {
			vm.renderEmptyEntryList(c, p)
			return
		}

		ui.Scroll(c.Key("cap-list-scroll")).Grow(1).MinHeight(0).Margin(0, -6, 0, 0).Children(func() {
			ui.Column(c).Gap(4).Padding(0, 6, 4, 0).Children(func() {
				for _, e := range filtered {
					vm.renderEntryRow(c, p, e)
				}
			})
		})
	})
}

func (vm *captureViewModel) renderEmptyEntryList(c *ui.Context, p palette) {
	ui.Column(c).Grow(1).Center().Padding(24, 18).Gap(10).Children(func() {
		ui.Box(c).Size(40, 40).Radius(11).Background(p.accentSoft).Center().Children(func() {
			ui.Icon(c, svgActivity).Size(20, 20).TextColor(p.accent)
		})
		if !vm.status.Enabled && len(vm.entries) == 0 {
			ui.Text(c, "抓包分析尚未开启").FontSize(13.5).Bold().TextColor(p.text)
			ui.Text(c, "开启后将自动透明接管本地代理端口：明文 HTTP 与 UDP（DNS / QUIC / STUN）可直接采集详情；HTTPS 默认加密透传，将域名加入白名单并信任本机根证书后即可展开 Query、Header 与 Body。").
				FontSize(11.5).TextColor(p.textMuted).Wrap()
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("cap-empty-start"), "立即开启抓包").Padding(6, 14).Radius(8).
					Background(p.accent).TextColor(ui.Hex("#ffffff")).Cursor(ui.CursorPointer).Clicked() {
					vm.togglingProxy = true
					vm.toggleCaptureProxy(true)
				}
				if ui.Button(c.Key("cap-empty-cert"), "配置 HTTPS 证书与白名单").Padding(6, 12).Radius(8).
					Cursor(ui.CursorPointer).Clicked() {
					vm.certModalOpen = true
				}
			})
		} else if len(vm.entries) == 0 {
			ui.Text(c, "正在等待网络请求…").FontSize(13).Bold().TextColor(p.text)
			ui.Text(c, fmt.Sprintf("经由本地代理 127.0.0.1:%d 的 TCP/HTTP(S) 与 SOCKS5 UDP 请求会实时显示在这里。", vm.status.ProxyPort)).
				FontSize(11.5).TextColor(p.textMuted).Wrap()
		} else {
			ui.Text(c, "没有匹配当前筛选条件的记录").FontSize(12.5).TextColor(p.textMuted)
		}
	})
}

func (vm *captureViewModel) renderEntryRow(c *ui.Context, p palette, e capture.Entry) {
	selected := e.ID == vm.selectedID
	bg := p.cardElevated
	bdr := p.border
	if selected {
		bg = p.cardSelected
		bdr = p.borderStrong
	}

	rowLabel := strings.TrimSpace(e.Method + " " + e.TargetAddr() + " " + e.DisplaySummary())
	row := ui.Column(c.Key(fmt.Sprintf("cap-row-%d", e.ID))).
		Padding(7, 9).Gap(3.5).Radius(8.5).Background(bg).Border(1, bdr).Cursor(ui.CursorPointer).
		Label(rowLabel)
	if !selected && row.Hovered() {
		row.Background(p.cardHover)
	}
	row.Children(func() {
		// 第一行：协议/方法徽标 + 状态码 + Host + 时间
		ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
			badgeText, badgeFg, badgeBg := captureBadgeStyle(e, p)
			renderMiniTag(c, badgeText, badgeFg, badgeBg)

			if e.StatusCode > 0 {
				scFg, scBg := statusCodeStyle(e.StatusCode, p)
				renderMiniTag(c, strconv.Itoa(e.StatusCode), scFg, scBg)
			} else if e.Error != "" {
				renderMiniTag(c, "ERR", p.danger, p.dangerSoft)
			}

			hostText := e.Host
			if hostText == "" {
				hostText = e.TargetAddr()
			}
			ui.Text(c, hostText).Grow(1).MinWidth(0).FontSize(12).Bold().TextColor(p.text).Ellipsis("…")
			ui.Text(c, e.StartedAt.Format("15:04:05")).FontSize(9.5).TextColor(p.textMuted).NoWrap()
		})

		// 第二行：Path / DNS / QUIC 摘要
		sum := e.DisplaySummary()
		if sum != "" && sum != e.Host {
			ui.Text(c, sum).FontSize(11).TextColor(p.textSecondary).Ellipsis("…")
		}

		// 第三行：进程 + 加解密标志 + 耗时 + 大小/包数
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			if e.ProcessName != "" {
				renderMiniTag(c, compactProcessLabel(e.ProcessName, 18), p.textSecondary, p.pillBg)
			}
			if e.Kind == capture.KindHTTPS {
				if e.MITMDecrypted {
					renderMiniTag(c, "已解密", p.success, p.successSoft)
				} else {
					renderMiniTag(c, "TLS 透传", p.textMuted, p.pillBg)
				}
			}
			ui.Spacer(c)
			if e.Kind == capture.KindUDP {
				ui.Text(c, fmt.Sprintf("↑%d ↓%d 包 · %s", e.PacketsSent, e.PacketsRecv, FormatBytes(e.BytesSent+e.BytesRecv))).
					FontSize(10).TextColor(p.textMuted).NoWrap()
			} else {
				if e.DurationMs > 0 {
					ui.Text(c, fmt.Sprintf("%d ms", e.DurationMs)).FontSize(10).TextColor(p.textMuted).NoWrap()
				}
				totalBytes := e.BytesRecv
				if totalBytes == 0 {
					totalBytes = e.BytesSent
				}
				ui.Text(c, FormatBytes(totalBytes)).FontSize(10).TextColor(p.textMuted).NoWrap()
			}
		})
	})

	if row.Clicked() {
		vm.selectedID = e.ID
		clear(vm.udpExpanded)
	}
}

func (vm *captureViewModel) findSelectedEntry() (capture.Entry, bool) {
	if vm.selectedID == 0 {
		return capture.Entry{}, false
	}
	for _, e := range vm.entries {
		if e.ID == vm.selectedID {
			return e, true
		}
	}
	return capture.Entry{}, false
}

func (vm *captureViewModel) renderDetailPane(c *ui.Context, p palette) {
	ui.Column(c).Grow(1).MinWidth(380).Gap(8).Radius(12).
		Background(p.card).Border(1, p.border).Padding(12).Children(func() {
		entry, ok := vm.findSelectedEntry()
		if !ok {
			ui.Column(c).Grow(1).Center().Gap(6).Children(func() {
				ui.Text(c, "选择左侧任意一条记录查看详细信息").FontSize(13).Bold().TextColor(p.textSecondary)
				ui.Text(c, "支持展开 HTTP/HTTPS 的 Query、Header、美化 JSON Body、复制 cURL，以及 UDP 的 DNS / QUIC / 数据报样本。").
					FontSize(11.5).TextColor(p.textMuted).Wrap()
			})
			return
		}

		// 顶部标题与就地快捷操作栏
		vm.renderDetailHeader(c, p, entry)

		if entry.Error != "" {
			ui.Row(c).Padding(7, 10).Radius(8).Background(p.dangerSoft).Border(1, p.danger).Children(func() {
				ui.Text(c, entry.Error).FontSize(11).TextColor(p.danger).Wrap()
			})
		}

		switch entry.Kind {
		case capture.KindUDP:
			vm.renderUDPDetail(c, p, entry)
		case capture.KindHTTPS:
			if entry.MITMDecrypted || entry.URL != "" {
				vm.renderHTTPDetail(c, p, entry)
			} else {
				vm.renderOpaqueTLSDetail(c, p, entry)
			}
		case capture.KindHTTP:
			vm.renderHTTPDetail(c, p, entry)
		default:
			vm.renderOpaqueTLSDetail(c, p, entry)
		}
	})
}

func (vm *captureViewModel) renderDetailHeader(c *ui.Context, p palette, e capture.Entry) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			badgeText, badgeFg, badgeBg := captureBadgeStyle(e, p)
			renderMiniTag(c, badgeText, badgeFg, badgeBg)
			if e.StatusCode > 0 {
				scFg, scBg := statusCodeStyle(e.StatusCode, p)
				renderMiniTag(c, fmt.Sprintf("%d %s", e.StatusCode, strings.TrimSpace(strings.TrimPrefix(e.StatusText, strconv.Itoa(e.StatusCode)))), scFg, scBg)
			}
			title := e.URL
			if title == "" {
				title = e.TargetAddr()
			}
			ui.Text(c, title).Grow(1).MinWidth(0).FontSize(12.5).Bold().TextColor(p.text).Ellipsis("…").Tooltip(title)
		})

		// 就地快捷操作栏
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			if e.URL != "" {
				if ui.Button(c.Key("det-copy-url"), "复制 URL").Padding(3, 8).Radius(6).
					Cursor(ui.CursorPointer).Clicked() {
					if vm.app != nil {
						mygo.Clipboard.WriteText(e.URL)
					}
					vm.showToast("已复制 URL")
				}
				if ui.Button(c.Key("det-copy-curl"), "复制 cURL").Padding(3, 8).Radius(6).
					Cursor(ui.CursorPointer).Clicked() {
					if curlCmd := capture.BuildCURL(e); curlCmd != "" {
						if vm.app != nil {
							mygo.Clipboard.WriteText(curlCmd)
						}
						vm.showToast("已复制 cURL 命令")
					}
				}
			}

			if e.Host != "" {
				if ui.Button(c.Key("det-add-direct"), "加直连").Padding(3, 8).Radius(6).
					TextColor(p.success).Cursor(ui.CursorPointer).Tooltip("为该域名添加直连分流规则").Clicked() {
					vm.quickAddRule(e.Host, 0)
				}
				if ui.Button(c.Key("det-add-proxy"), "加代理").Padding(3, 8).Radius(6).
					TextColor(p.accent).Cursor(ui.CursorPointer).Tooltip("为该域名添加代理分流规则").Clicked() {
					vm.quickAddRule(e.Host, 2)
				}

				if e.Kind == capture.KindHTTPS {
					inWL := capture.MatchMITMHost(e.Host, vm.status.MITMHosts)
					wlLabel := "加入解密白名单"
					if inWL {
						wlLabel = "移出解密白名单"
					}
					if ui.Button(c.Key("det-mitm-wl"), wlLabel).Padding(3, 8).Radius(6).
						Cursor(ui.CursorPointer).Clicked() {
						host := e.Host
						if inWL {
							vm.runAsync(func() error {
								_, err := vm.app.eng.RemoveMITMHost(host)
								return err
							}, "已从解密白名单移除 "+host)
						} else {
							vm.addHostToMITMWhitelist(host)
						}
					}
				}
			}
		})
		ui.Box(c).Height(1).Background(p.border)
	})
}

func (vm *captureViewModel) addHostToMITMWhitelist(host string) {
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	go func() {
		_, err := vm.app.eng.AddMITMHost(host)
		st := vm.app.eng.CaptureStatus()
		vm.app.updateCaptureWindow(func() {
			vm.status = st
			if err != nil {
				vm.showToast(err.Error())
				return
			}
			if !st.Cert.Trusted || !st.MITMEnabled {
				vm.certModalOpen = true
				vm.showToast("已加入白名单，请确认启用解密并安装信任根证书")
			} else {
				vm.showToast("已加入解密白名单：" + host + "（新连接生效）")
			}
		})
	}()
}

func (vm *captureViewModel) quickAddRule(host string, action int) {
	domain, matching := suggestRuleFromTarget(host)
	if domain == "" {
		return
	}
	actionName := "代理"
	if action == 0 {
		actionName = "直连"
	}
	vm.runAsync(func() error {
		_, err := vm.app.eng.AddCustomRule(config.DomainRule{
			Domain:    domain,
			Matching:  matching,
			ProxyType: action,
			Label:     "抓包快捷",
		})
		return err
	}, fmt.Sprintf("已添加%s规则: %s", actionName, domain))
}

func (vm *captureViewModel) renderHTTPDetail(c *ui.Context, p palette, e capture.Entry) {
	qLabel := fmt.Sprintf("Query (%d)", len(e.QueryParams))
	renderSegmented(c.Key("cap-detail-tabs"), p, &vm.detailTab, "概览", qLabel, "请求", "响应")

	ui.Scroll(c.Key("cap-detail-scroll")).Grow(1).MinHeight(0).Margin(0, -6, 0, 0).Children(func() {
		ui.Column(c).Gap(10).Padding(2, 6, 6, 0).Children(func() {
			switch vm.detailTab {
			case 0:
				vm.renderHTTPOverviewTab(c, p, e)
			case 1:
				vm.renderKVSection(c, p, "URL Query 参数", e.QueryParams, "本次请求没有 Query 参数")
			case 2:
				vm.renderCollapsibleKVSection(c, p, "req-hdrs", fmt.Sprintf("Request Headers (%d)", len(e.ReqHeaders)), e.ReqHeaders, "无请求头", &vm.reqHeadersCollapsed)
				vm.renderBodySection(c, p, "req-body", "Request Body", e.ReqBody, e.ReqBodySize, e.ReqBodyTruncated, e.ReqContentType)
			case 3:
				vm.renderCollapsibleKVSection(c, p, "resp-hdrs", fmt.Sprintf("Response Headers (%d)", len(e.RespHeaders)), e.RespHeaders, "无响应头", &vm.respHeadersCollapsed)
				vm.renderBodySection(c, p, "resp-body", "Response Body", e.RespBody, e.RespBodySize, e.RespBodyTruncated, e.RespContentType)
			}
		})
	})
}

func (vm *captureViewModel) renderHTTPOverviewTab(c *ui.Context, p palette, e capture.Entry) {
	rows := []capture.KV{
		{Key: "请求 URL", Value: nonEmpty(e.URL, e.TargetAddr())},
		{Key: "方法 / 协议", Value: strings.TrimSpace(e.Method + " · " + e.Protocol)},
		{Key: "状态码", Value: nonEmpty(e.StatusText, "-")},
		{Key: "目标主机", Value: e.TargetAddr()},
		{Key: "客户端进程", Value: nonEmpty(e.ProcessName, "未识别")},
		{Key: "客户端地址", Value: nonEmpty(e.ClientAddr, "-")},
		{Key: "请求时间", Value: e.StartedAt.Format("2006-01-02 15:04:05.000")},
		{Key: "耗时", Value: fmt.Sprintf("%d ms", e.DurationMs)},
		{Key: "请求 / 响应体积", Value: fmt.Sprintf("↑ %s / ↓ %s", FormatBytes(e.ReqBodySize), FormatBytes(e.RespBodySize))},
	}
	if e.Kind == capture.KindHTTPS {
		mitmLabel := "未解密（透传）"
		if e.MITMDecrypted {
			mitmLabel = "已通过白名单本地 CA 解密"
		}
		rows = append(rows,
			capture.KV{Key: "HTTPS 解密", Value: mitmLabel},
			capture.KV{Key: "TLS 版本 / ALPN", Value: nonEmpty(strings.Trim(e.TLSVersion+" · "+e.TLSALPN, " ·"), "-")},
		)
	}
	vm.renderKVSection(c, p, "请求概览", rows, "")
}

func (vm *captureViewModel) renderOpaqueTLSDetail(c *ui.Context, p palette, e capture.Entry) {
	ui.Scroll(c.Key("cap-tls-scroll")).Grow(1).MinHeight(0).Margin(0, -6, 0, 0).Children(func() {
		ui.Column(c).Gap(10).Padding(2, 6, 6, 0).Children(func() {
			if e.Kind == capture.KindHTTPS {
				ui.Column(c).Padding(10, 12).Gap(7).Radius(10).
					Background(p.accentSoft).Border(1, p.borderStrong).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Icon(c, svgLock).Size(13, 13).TextColor(p.accent)
						ui.Text(c, "当前 HTTPS 连接未解密（仅显示 TLS 握手元信息）").FontSize(12).Bold().TextColor(p.accent)
					})
					ui.Text(c, "为保护隐私与避免证书锁定应用异常，默认仅对白名单域名执行 HTTPS 解密。若需查看该域名的请求 Path、Query、Header 与 Body，请先将其加入白名单并信任本机根证书。").
						FontSize(11).TextColor(p.textSecondary).Wrap()
					ui.Row(c).Gap(8).Children(func() {
						if e.Host != "" && !capture.MatchMITMHost(e.Host, vm.status.MITMHosts) {
							if ui.Button(c.Key("opaque-add-wl"), "将 "+e.Host+" 加入白名单").Padding(4, 10).Radius(7).
								Background(p.accent).TextColor(ui.Hex("#ffffff")).Cursor(ui.CursorPointer).Clicked() {
								vm.addHostToMITMWhitelist(e.Host)
							}
						}
						if ui.Button(c.Key("opaque-open-cert"), "打开证书与白名单设置").Padding(4, 10).Radius(7).
							Cursor(ui.CursorPointer).Clicked() {
							vm.certModalOpen = true
						}
					})
				})
			}

			rows := []capture.KV{
				{Key: "目标地址", Value: e.TargetAddr()},
				{Key: "协议", Value: e.Protocol},
				{Key: "TLS SNI", Value: nonEmpty(e.TLSSNI, "-")},
				{Key: "TLS 版本", Value: nonEmpty(e.TLSVersion, "-")},
				{Key: "TLS ALPN", Value: nonEmpty(e.TLSALPN, "-")},
				{Key: "客户端进程", Value: nonEmpty(e.ProcessName, "未识别")},
				{Key: "客户端地址", Value: nonEmpty(e.ClientAddr, "-")},
				{Key: "开始时间", Value: e.StartedAt.Format("2006-01-02 15:04:05.000")},
				{Key: "持续时长", Value: fmt.Sprintf("%d ms", e.DurationMs)},
				{Key: "流量统计", Value: fmt.Sprintf("上行 %s · 下行 %s", FormatBytes(e.BytesSent), FormatBytes(e.BytesRecv))},
			}
			vm.renderKVSection(c, p, "连接与握手详情", rows, "")
		})
	})
}

func (vm *captureViewModel) renderUDPDetail(c *ui.Context, p palette, e capture.Entry) {
	ui.Scroll(c.Key("cap-udp-scroll")).Grow(1).MinHeight(0).Margin(0, -6, 0, 0).Children(func() {
		ui.Column(c).Gap(10).Padding(2, 6, 6, 0).Children(func() {
			rows := []capture.KV{
				{Key: "识别协议", Value: nonEmpty(e.UDPProtocol, "UDP")},
				{Key: "目标地址", Value: e.TargetAddr()},
				{Key: "客户端进程", Value: nonEmpty(e.ProcessName, "未识别")},
				{Key: "客户端地址", Value: nonEmpty(e.ClientAddr, "-")},
				{Key: "收发包统计", Value: fmt.Sprintf("发送 %d 包 (%s) · 接收 %d 包 (%s)", e.PacketsSent, FormatBytes(e.BytesSent), e.PacketsRecv, FormatBytes(e.BytesRecv))},
				{Key: "会话时长", Value: fmt.Sprintf("%d ms", e.DurationMs)},
			}
			vm.renderKVSection(c, p, "UDP 会话概览", rows, "")

			if e.QUICInfo != nil {
				qRows := []capture.KV{
					{Key: "QUIC 版本", Value: nonEmpty(e.QUICInfo.Version, "-")},
					{Key: "解析 SNI 域名", Value: nonEmpty(e.QUICInfo.SNI, "未提取到明文 Initial SNI")},
					{Key: "ALPN 协议", Value: nonEmpty(e.QUICInfo.ALPN, "-")},
				}
				vm.renderKVSection(c, p, "QUIC / HTTP3 握手元信息", qRows, "")
			}

			if len(e.DNSRecords) > 0 {
				ui.Column(c).Padding(10).Gap(6).Radius(10).Background(p.cardElevated).Border(1, p.border).Children(func() {
					ui.Text(c, fmt.Sprintf("DNS 解析记录 (%d)", len(e.DNSRecords))).FontSize(11.5).Bold().TextColor(p.textMuted)
					for i, rec := range e.DNSRecords {
						ui.Column(c.Key(fmt.Sprintf("dns-%d", i))).Padding(6, 8).Gap(2).Radius(7).
							Background(p.card).Border(1, p.border).Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
								dirLabel, fg, bg := "查询", p.accent, p.accentSoft
								if rec.Direction == "answer" {
									dirLabel, fg, bg = "响应", p.success, p.successSoft
								}
								renderMiniTag(c, dirLabel, fg, bg)
								renderMiniTag(c, rec.QType, p.textSecondary, p.pillBg)
								ui.Text(c, rec.Name).Grow(1).FontSize(11.5).Bold().TextColor(p.text).Ellipsis("…")
								if rec.RCode != "" {
									ui.Text(c, rec.RCode).FontSize(10).TextColor(p.textMuted).NoWrap()
								}
							})
							if len(rec.Answers) > 0 {
								ui.Text(c, "解析结果："+strings.Join(rec.Answers, ", ")).
									FontSize(11).TextColor(p.textSecondary).Wrap()
							}
						})
					}
				})
			}

			if len(e.UDPSamples) > 0 {
				ui.Column(c).Padding(10).Gap(6).Radius(10).Background(p.cardElevated).Border(1, p.border).Children(func() {
					ui.Text(c, fmt.Sprintf("最近 UDP 数据报样本 (%d，点击展开 Hex 预览)", len(e.UDPSamples))).
						FontSize(11.5).Bold().TextColor(p.textMuted)
					for i := len(e.UDPSamples) - 1; i >= 0; i-- {
						s := e.UDPSamples[i]
						expanded := vm.udpExpanded[i]
						card := ui.Column(c.Key(fmt.Sprintf("udp-s-%d", i))).Padding(6, 8).Gap(4).Radius(7).
							Background(p.card).Border(1, p.border)
						card.Children(func() {
							hdr := ui.Row(c.Key("hdr")).AlignItems(ui.Center).Gap(6).Cursor(ui.CursorPointer).
								Label(nonEmpty(s.Summary, fmt.Sprintf("UDP 样本 %d B", s.Size)))
							hdr.Children(func() {
								dir, fg, bg := "↑ 发", p.accent, p.accentSoft
								if s.Direction == "in" {
									dir, fg, bg = "↓ 收", p.success, p.successSoft
								}
								renderMiniTag(c, dir, fg, bg)
								ui.Text(c, nonEmpty(s.Summary, fmt.Sprintf("%d B", s.Size))).
									Grow(1).FontSize(11).TextColor(p.text).Ellipsis("…")
								ui.Text(c, fmt.Sprintf("%d B · %s", s.Size, s.Timestamp.Format("15:04:05.000"))).
									FontSize(10).TextColor(p.textMuted).NoWrap()
							})
							if hdr.Clicked() {
								vm.udpExpanded[i] = !expanded
							}
							if expanded && len(s.Preview) > 0 {
								ui.Box(c).Padding(7, 8).Radius(6).Background(p.canvas).Border(1, p.border).Children(func() {
									ui.Text(c, capture.FormatHexPreview(s.Preview)).Font("monospace").FontSize(10.5).TextColor(p.textSecondary).Wrap()
								})
							}
						})
					}
				})
			}
		})
	})
}

func (vm *captureViewModel) renderKVSection(c *ui.Context, p palette, title string, items []capture.KV, emptyText string) {
	vm.renderCollapsibleKVSection(c, p, title, title, items, emptyText, nil)
}

func (vm *captureViewModel) renderCollapsibleKVSection(c *ui.Context, p palette, key, title string, items []capture.KV, emptyText string, collapsed *bool) {
	isCollapsed := collapsed != nil && *collapsed
	ui.Column(c.Key("kvsec-"+key)).Padding(9, 10).Gap(4).Radius(10).Background(p.cardElevated).Border(1, p.border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, title).FontSize(11.5).Bold().TextColor(p.textMuted)
			ui.Spacer(c)
			if collapsed != nil && len(items) > 0 {
				toggleLabel := "收起"
				if isCollapsed {
					toggleLabel = "展开"
				}
				toggleBtn := ui.Row(c.Key("toggle")).Padding(1.5, 7).Radius(5).
					Background(p.pillBg).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(toggleLabel + title)
				if toggleBtn.Hovered() {
					toggleBtn.Background(p.cardHover)
				}
				toggleBtn.Children(func() {
					ui.Text(c, toggleLabel).FontSize(10.5).TextColor(p.textSecondary).NoWrap()
				})
				if toggleBtn.Clicked() {
					*collapsed = !isCollapsed
				}
			}
		})
		if isCollapsed {
			return
		}
		if len(items) == 0 {
			ui.Text(c, emptyText).FontSize(11.5).TextColor(p.textMuted)
			return
		}
		for i, kv := range items {
			row := ui.Row(c.Key(fmt.Sprintf("kv-%s-%d", key, i))).
				Padding(2.5, 6).Gap(10).Radius(5.5).AlignItems(ui.Start).Cursor(ui.CursorPointer).Tooltip("点击复制值")
			if row.Hovered() {
				row.Background(p.cardHover)
			}
			row.Children(func() {
				ui.Text(c, kv.Key).Width(170).FontSize(11).Bold().TextColor(p.textSecondary).Wrap()
				ui.Text(c, kv.Value).Grow(1).MinWidth(0).FontSize(11).TextColor(p.text).Wrap()
			})
			if row.Clicked() {
				if vm.app != nil {
					mygo.Clipboard.WriteText(kv.Value)
				}
				vm.showToast("已复制 " + kv.Key)
			}
		}
	})
}

func (vm *captureViewModel) renderBodySection(c *ui.Context, p palette, key, title string, body []byte, totalSize int64, truncated bool, contentType string) {
	formatted, kindLabel := capture.FormatBodyForView(body, contentType)

	ui.Column(c.Key(key)).Padding(10).Gap(6).Radius(10).Background(p.cardElevated).Border(1, p.border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, title).FontSize(11.5).Bold().TextColor(p.textMuted)
			renderMiniTag(c, kindLabel, p.accent, p.accentSoft)
			if totalSize > 0 {
				sizeNote := FormatBytes(totalSize)
				if truncated {
					sizeNote += "（已截断预览）"
				}
				ui.Text(c, sizeNote).FontSize(10.5).TextColor(p.textMuted)
			}
			ui.Spacer(c)
			if formatted != "" {
				if ui.Button(c.Key(key+"-copy"), "复制 Body").Padding(2.5, 8).Radius(6).
					Cursor(ui.CursorPointer).Clicked() {
					if vm.app != nil {
						mygo.Clipboard.WriteText(formatted)
					}
					vm.showToast("已复制 " + title)
				}
			}
		})
		if formatted == "" {
			ui.Text(c, "（空 Body）").FontSize(11.5).TextColor(p.textMuted)
			return
		}
		// 避免超长单段文本拖慢布局，界面预览上限 64 KiB（复制按钮仍可复制全部已捕获内容）
		display := formatted
		if len(display) > 64*1024 {
			display = display[:64*1024] + "\n\n...（界面预览已截断，可点击右上角“复制 Body”查看完整内容）"
		}
		ui.Box(c).Padding(8, 10).Radius(8).Background(p.canvas).Border(1, p.border).Children(func() {
			ui.Text(c, display).Font("monospace").FontSize(11).TextColor(p.text).Wrap()
		})
	})
}

// renderCertModal 绘制“HTTPS 白名单解密与自签名根证书”引导和配置弹窗，
// 重点向用户透明说明为何需要自签证书、隐私边界以及操作系统安全确认弹窗提示。
func (vm *captureViewModel) renderCertModal(c *ui.Context, p palette) {
	if !vm.certModalOpen {
		return
	}

	winW, winH := c.Size()
	panelW := min(float32(660), max(float32(460), winW-48))
	panelH := min(float32(660), max(float32(460), winH-48))
	backdropAlpha := float32(0.42)
	if c.Theme().Dark {
		backdropAlpha = 0.58
	}

	ui.DialogBase(c.Key("cap-cert-modal"), &vm.certModalOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, backdropAlpha))
		panel.Width(panelW).Height(panelH).Padding(16).Gap(10).Radius(14).
			Background(p.card).Border(1, p.borderStrong).
			Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.28))

		// 标题栏
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Box(c).Size(26, 26).Radius(7.5).Background(p.accentSoft).Center().Children(func() {
				ui.Icon(c, svgShield).Size(14, 14).TextColor(p.accent)
			})
			ui.Text(c, "HTTPS 白名单解密与自签名根证书").FontSize(14.5).Bold().TextColor(p.text)
			ui.Spacer(c)
			closeBtn := ui.Box(c.Key("cert-modal-close")).Size(26, 26).Radius(6.5).Center().
				Cursor(ui.CursorPointer).Label("关闭").Tooltip("关闭")
			if closeBtn.Hovered() {
				closeBtn.Background(p.pillBg)
			}
			closeBtn.Children(func() {
				ui.Icon(c, svgClose).Size(13, 13).TextColor(p.textMuted)
			})
			if closeBtn.Clicked() {
				vm.certModalOpen = false
			}
		})

		ui.Scroll(c.Key("cert-modal-scroll")).Grow(1).MinHeight(0).Margin(0, -6, 0, 0).Children(func() {
			ui.Column(c).Gap(12).Padding(2, 6, 6, 0).Children(func() {
				// 1. 为什么需要安装自签证书 + 安全弹窗提前提醒
				ui.Column(c).Padding(11, 13).Gap(6).Radius(10).
					Background(p.warningSoft).Border(1, p.warning).Children(func() {
					ui.Text(c, "为什么需要安装自签名根证书（安全须知）").FontSize(12).Bold().TextColor(p.warning)
					ui.Text(c, "• HTTPS 流量默认端到端加密，代理只能看到访问域名而无法读取 URL Path、Query、Header 和 Body。安装本机自签名根证书后，FlyShadow Bar 才能在本地临时解密并还原请求内容。").
						FontSize(11).TextColor(p.text).Wrap()
					certDirHint := "~/.flyshadowbar/mitm/"
					if runtime.GOOS == "windows" {
						certDirHint = `%USERPROFILE%\.flyshadowbar\mitm\`
					}
					if vm.status.Cert.CertPath != "" {
						certDirHint = filepath.Dir(vm.status.Cert.CertPath)
					}
					ui.Text(c, fmt.Sprintf("• 隐私与隔离：根证书私钥仅在当前设备随机生成并仅限本机访问（保存于 %s），绝不上传云端；抓包记录仅存本机内存不落盘；采用严格白名单模式，未加入白名单的域名一律加密透传。", certDirHint)).
						FontSize(11).TextColor(p.text).Wrap()
					promptHint := vm.status.Cert.InstallPromptHint
					if promptHint == "" {
						promptHint = "点击安装后会弹出操作系统原生安全授权/警告窗口，确认信任后即生效；随时可在此一键卸载。"
					}
					ui.Text(c, "• 系统安全弹窗提醒："+promptHint).FontSize(11).Bold().TextColor(p.text).Wrap()
				})

				// 2. 证书状态与双端一键安装/卸载区
				cert := vm.status.Cert
				ui.Column(c).Padding(11, 13).Gap(8).Radius(10).
					Background(p.cardElevated).Border(1, p.border).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
						ui.Text(c, "本机根证书状态").FontSize(12).Bold().TextColor(p.text)
						if cert.Trusted {
							renderPill(c, "已安装并受系统信任", p.success, p.successSoft)
						} else if cert.Installed {
							renderPill(c, "已导入但未信任", p.warning, p.warningSoft)
						} else if cert.Generated {
							renderPill(c, "已生成 · 未安装到系统", p.warning, p.warningSoft)
						} else {
							renderPill(c, "尚未生成", p.textMuted, p.pillBg)
						}
						ui.Spacer(c)
						if ui.Button(c.Key("cert-refresh"), "刷新状态").Padding(3, 8).Radius(6).
							Cursor(ui.CursorPointer).Clicked() {
							vm.refreshCertStatusAsync()
						}
					})

					if cert.StatusDetail != "" {
						ui.Text(c, cert.StatusDetail).FontSize(11).TextColor(p.textSecondary).Wrap()
					}
					if cert.Generated {
						ui.Text(c, "证书路径："+cert.CertPath).FontSize(10.5).TextColor(p.textMuted).Wrap()
						if cert.FingerprintSHA256 != "" {
							fp := cert.FingerprintSHA256
							if len(fp) > 32 {
								fp = fp[:32] + "…"
							}
							ui.Text(c, fmt.Sprintf("SHA-256 指纹：%s · 有效期至 %s", fp, cert.NotAfter.Local().Format("2006-01-02"))).
								FontSize(10.5).TextColor(p.textMuted).Wrap()
						}
					}

					if vm.certActionErr != "" {
						ui.Text(c, vm.certActionErr).FontSize(11).TextColor(p.danger).Wrap()
					}

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						installLabel := "一键安装并信任证书"
						if cert.Trusted {
							installLabel = "重新安装并信任"
						}
						instBtn := ui.Button(c.Key("cert-install"), installLabel).Padding(5, 12).Radius(7).
							Background(p.accent).TextColor(ui.Hex("#ffffff")).Disabled(vm.certBusy)
						applyPointerCursor(instBtn, vm.certBusy)
						if instBtn.Clicked() {
							vm.installCertAsync()
						}

						if cert.Installed || cert.Trusted {
							uninstBtn := ui.Button(c.Key("cert-uninstall"), "一键卸载证书").Padding(5, 11).Radius(7).
								TextColor(p.danger).Disabled(vm.certBusy)
							applyPointerCursor(uninstBtn, vm.certBusy)
							if uninstBtn.Clicked() {
								vm.uninstallCertAsync()
							}
						}

						if cert.CertPath != "" {
							openDirBtn := ui.Row(c.Key("cert-open-dir")).Padding(5, 9).Radius(7).
								Background(p.card).Border(1, p.border).AlignItems(ui.Center).Gap(4).
								Cursor(ui.CursorPointer).Tooltip("在文件管理器中打开证书目录")
							if openDirBtn.Hovered() {
								openDirBtn.Background(p.cardHover)
							}
							openDirBtn.Children(func() {
								ui.Icon(c, svgFolder).Size(12, 12).TextColor(p.textSecondary)
								ui.Text(c, "打开目录").FontSize(11).TextColor(p.textSecondary).NoWrap()
							})
							if openDirBtn.Clicked() {
								target := cert.CertPath
								if !cert.Generated {
									target = filepath.Dir(cert.CertPath)
								}
								go func() { _ = revealPathInFileManager(target) }()
							}
						}
					})
				})

				// 3. 白名单域名管理区
				ui.Column(c).Padding(11, 13).Gap(8).Radius(10).
					Background(p.cardElevated).Border(1, p.border).Children(func() {
					mitmOn := vm.status.MITMEnabled
					renderSettingSwitch(c, p, "cap-mitm-enable", "启用白名单 HTTPS 解密",
						"仅对下方列出的域名执行本机解密，其余 HTTPS 流量一律透传（修改后对新建连接生效）",
						&mitmOn, func(v bool) {
							vm.runAsync(func() error { return vm.app.eng.SetMITMEnabled(v) }, toggleToast("白名单 HTTPS 解密", v))
						})

					if vm.status.MITMEnabled && !cert.Trusted {
						ui.Row(c).Padding(6, 9).Radius(7).Background(p.dangerSoft).Children(func() {
							ui.Text(c, "当前已开启 HTTPS 解密，但本机根证书尚未受系统信任，请先点击上方“一键安装并信任证书”，否则浏览器会提示证书不受信任。").
								FontSize(11).TextColor(p.danger).Wrap()
						})
					}

					// 添加域名输入框
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.TextInput(c.Key("mitm-host-input"), &vm.mitmHostDraft).Grow(1).
							Placeholder("输入域名或通配符，如 github.com 或 *.openai.com")
						if ui.Button(c.Key("mitm-host-add"), "添加白名单").Padding(5, 11).Radius(7).
							Background(p.accent).TextColor(ui.Hex("#ffffff")).Cursor(ui.CursorPointer).Clicked() {
							draft := strings.TrimSpace(vm.mitmHostDraft)
							if draft == "" {
								vm.showToast("请输入要解密的域名")
							} else {
								vm.mitmHostDraft = ""
								vm.runAsync(func() error {
									_, err := vm.app.eng.AddMITMHost(draft)
									return err
								}, "已加入白名单："+draft)
							}
						}
					})

					if len(vm.status.MITMHosts) == 0 {
						ui.Text(c, "白名单当前为空。添加域名（例如 github.com、*.example.com）后，该域名的 HTTPS 请求将可展开查看 Query、Header 和 Body。").
							FontSize(11).TextColor(p.textMuted).Wrap()
					} else {
						ui.Column(c).Gap(4).Children(func() {
							for _, host := range vm.status.MITMHosts {
								h := host
								ui.Row(c.Key("wl-"+h)).Padding(5, 9).Radius(7).Background(p.card).Border(1, p.border).
									AlignItems(ui.Center).Gap(8).Children(func() {
									ui.Icon(c, svgUnlock).Size(12, 12).TextColor(p.success)
									ui.Text(c, h).Grow(1).FontSize(11.5).TextColor(p.text)
									rm := ui.Box(c.Key("rm")).Size(20, 20).Radius(5).Center().
										Cursor(ui.CursorPointer).Tooltip("移出白名单")
									if rm.Hovered() {
										rm.Background(p.dangerSoft)
									}
									rm.Children(func() {
										ui.Icon(c, svgClose).Size(11.5, 11.5).TextColor(p.danger)
									})
									if rm.Clicked() {
										vm.runAsync(func() error {
											_, err := vm.app.eng.RemoveMITMHost(h)
											return err
										}, "已移除 "+h)
									}
								})
							}
						})
					}
				})

				// 4. 自带证书库的 CLI / 开发工具辅助提示（Node.js / Python / Git）
				if len(cert.EnvSnippets) > 0 {
					ui.Column(c).Padding(11, 13).Gap(6).Radius(10).
						Background(p.cardElevated).Border(1, p.border).Children(func() {
						ui.Text(c, "Node.js / Python / Git 等命令行工具适配（可选）").FontSize(11.5).Bold().TextColor(p.textMuted)
						browserHint := "Chrome、Edge、Safari 与系统原生应用在信任根证书后会自动生效；若命令行工具自带独立 CA 证书库，可复制以下配置："
						if runtime.GOOS == "windows" {
							browserHint = "Edge、Chrome 与系统原生应用在信任根证书后会自动生效；若命令行工具自带独立 CA 证书库，可复制以下配置："
						}
						ui.Text(c, browserHint).
							FontSize(10.5).TextColor(p.textMuted).Wrap()
						for i, sn := range cert.EnvSnippets {
							s := sn
							ui.Row(c.Key(fmt.Sprintf("env-sn-%d", i))).Padding(5, 8).Radius(7).
								Background(p.card).Border(1, p.border).AlignItems(ui.Center).Gap(8).Children(func() {
								ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
									ui.Text(c, s.Label).FontSize(10.5).Bold().TextColor(p.textSecondary)
									ui.Text(c, s.Value).FontSize(10.5).TextColor(p.textMuted).Ellipsis("…")
								})
								if ui.Button(c.Key("copy"), "复制").Padding(2.5, 8).Radius(5.5).
									Cursor(ui.CursorPointer).Clicked() {
									if vm.app != nil {
										mygo.Clipboard.WriteText(s.Value)
									}
									vm.showToast("已复制 " + s.Label + " 配置")
								}
							})
						}
					})
				}
			})
		})
	})
}

func (vm *captureViewModel) refreshCertStatusAsync() {
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	go func() {
		cert := vm.app.eng.RefreshMITMCertStatus()
		st := vm.app.eng.CaptureStatus()
		st.Cert = cert
		vm.app.updateCaptureWindow(func() {
			vm.status = st
			vm.showToast("已刷新证书状态")
		})
	}()
}

func (vm *captureViewModel) installCertAsync() {
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	vm.certBusy = true
	vm.certActionErr = ""
	go func() {
		cert, err := vm.app.eng.InstallMITMCA()
		st := vm.app.eng.CaptureStatus()
		st.Cert = cert
		vm.app.updateCaptureWindow(func() {
			vm.certBusy = false
			vm.status = st
			if err != nil {
				vm.certActionErr = err.Error()
				vm.showToast("证书安装未完成")
			} else if cert.Trusted {
				vm.certActionErr = ""
				vm.showToast("本机自签根证书已安装并受系统信任")
			} else {
				vm.certActionErr = cert.StatusDetail
				vm.showToast("证书已导入，请确认系统信任状态")
			}
		})
	}()
}

func (vm *captureViewModel) uninstallCertAsync() {
	if vm.app == nil || vm.app.eng == nil {
		return
	}
	vm.certBusy = true
	vm.certActionErr = ""
	go func() {
		cert, err := vm.app.eng.UninstallMITMCA()
		st := vm.app.eng.CaptureStatus()
		st.Cert = cert
		vm.app.updateCaptureWindow(func() {
			vm.certBusy = false
			vm.status = st
			if err != nil {
				vm.certActionErr = err.Error()
				vm.showToast("卸载证书失败")
			} else {
				vm.certActionErr = ""
				vm.showToast("已从系统根证书存储卸载本机 CA")
			}
		})
	}()
}

func captureBadgeStyle(e capture.Entry, p palette) (label string, fg, bg ui.Color) {
	switch e.Kind {
	case capture.KindUDP:
		lbl := e.UDPProtocol
		if lbl == "" {
			lbl = "UDP"
		}
		if lbl == "DNS" || lbl == "mDNS" {
			return lbl, p.success, p.successSoft
		}
		if lbl == "QUIC" {
			return lbl, p.accent, p.accentSoft
		}
		return lbl, p.warning, p.warningSoft
	case capture.KindHTTP, capture.KindHTTPS:
		if e.Method != "" {
			switch e.Method {
			case "GET":
				return "GET", p.accent, p.accentSoft
			case "POST":
				return "POST", p.success, p.successSoft
			case "PUT", "PATCH":
				return e.Method, p.warning, p.warningSoft
			case "DELETE":
				return "DELETE", p.danger, p.dangerSoft
			default:
				return e.Method, p.textSecondary, p.pillBg
			}
		}
		return "HTTPS", p.accent, p.accentSoft
	default:
		return nonEmpty(e.Protocol, "TCP"), p.textSecondary, p.pillBg
	}
}

func statusCodeStyle(code int, p palette) (fg, bg ui.Color) {
	switch {
	case code >= 200 && code < 300:
		return p.success, p.successSoft
	case code >= 300 && code < 400:
		return p.accent, p.accentSoft
	case code >= 400 && code < 500:
		return p.warning, p.warningSoft
	default:
		return p.danger, p.dangerSoft
	}
}
