package ui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

// renderUpdateTopBanner 在弹窗顶部展示后台或手动更新的下载进度条，以及安装就绪后的一键重启提示。
func (vm *viewModel) renderUpdateTopBanner(c *ui.Context, p palette) {
	if vm.updateDownloading && vm.updateAvailable != "" {
		title, detail, pctLabel, ratio := vm.updateProgressState()
		ui.Column(c).Padding(8, 12).Gap(6).Radius(11).
			Background(p.accentSoft).Border(1, p.borderStrong).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, title).FontSize(12).Bold().TextColor(p.accent).Ellipsis("…")
					if detail != "" {
						ui.Text(c, detail).FontSize(10).TextColor(p.textSecondary).NoWrap()
					}
				})
				if pctLabel != "" {
					ui.Text(c, pctLabel).FontSize(11).Bold().TextColor(p.accent).NoWrap()
				}
				if ui.Button(c.Key("btn-banner-cancel-update"), "取消").Padding(4, 9).Radius(6).Clicked() {
					vm.cancelUpdateDownload()
				}
			})
			ui.Progress(c.Key("banner-update-progress"), ratio).
				Height(4.5).Radius(99).Background(p.pillBg).TextColor(p.accent).Label("更新下载进度")
		})
		return
	}

	if readyVer := vm.readyUpdateVersion(); vm.updateInstalled && readyVer != "" {
		subText := "重启后生效"
		hasNewer := vm.hasSupersedingUpdate()
		if hasNewer {
			if vm.updateCanReinstall {
				subText = fmt.Sprintf("线上已有更新版 %s，可直接覆盖或立即重启", vm.updateAvailable)
			} else {
				subText = fmt.Sprintf("线上已有更新版 %s，重启后将继续升级", vm.updateAvailable)
			}
		}
		ui.Row(c).Padding(8, 12).Gap(10).Radius(11).AlignItems(ui.Center).
			Background(p.successSoft).Border(1, p.success).Children(func() {
			ui.Box(c).Size(8, 8).Radius(99).Background(p.success)
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				ui.Text(c, fmt.Sprintf("新版本 %s 已就绪", readyVer)).
					FontSize(12.5).Bold().TextColor(p.success)
				ui.Text(c, subText).
					FontSize(10.5).TextColor(p.textSecondary).Wrap()
			})
			if hasNewer && vm.updateCanReinstall {
				if ui.Button(c.Key("btn-banner-supersede"), "下新版").Padding(5, 10).Radius(7).
					Tooltip("覆盖下载最新版本 " + vm.updateAvailable).Clicked() {
					vm.installNativeUpdate()
				}
			}
			if ui.PrimaryButton(c.Key("btn-banner-restart"), "立即重启").Padding(5, 12).Radius(7).Clicked() {
				vm.restartForUpdate()
			}
		})
	}
}

// renderUpdateSection 渲染设置页「关于与更新」底部的版本状态、操作按钮、下载进度条和 Markdown 更新说明。
func (vm *viewModel) renderUpdateSection(c *ui.Context, p palette) {
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			updateText := fmt.Sprintf("当前版本 %s", version.Version)
			updateColor := p.textMuted
			readyVer := vm.readyUpdateVersion()
			hasNewer := vm.hasSupersedingUpdate()
			switch {
			case vm.updateDownloading:
				updateText, _, _, _ = vm.updateProgressState()
				updateColor = p.accent
			case vm.checkingUpdate:
				updateText = "检查更新中…"
				updateColor = p.accent
			case vm.updateErr != "":
				updateText = vm.updateErr
				updateColor = p.danger
			case vm.updateInstalled:
				if hasNewer {
					if vm.updateCanReinstall {
						updateText = fmt.Sprintf("新版本 %s 已就绪，线上已有更新版 %s", readyVer, vm.updateAvailable)
					} else {
						updateText = fmt.Sprintf("新版本 %s 已就绪（线上已有 %s，重启后将继续升级）", readyVer, vm.updateAvailable)
					}
				} else {
					updateText = fmt.Sprintf("新版本 %s 已就绪，重启后生效", readyVer)
				}
				updateColor = p.success
			case vm.updateNativeEnabled && vm.updateAvailable != "":
				updateText = fmt.Sprintf("有新版本 %s", vm.updateAvailable)
				updateColor = p.success
			case vm.updateResult != nil:
				if vm.updateResult.HasUpdate {
					updateText = fmt.Sprintf("有新版本 %s", vm.updateResult.LatestVersion)
					updateColor = p.success
				} else {
					updateText = "已是最新版本"
					updateColor = p.textSecondary
				}
			}
			ui.Text(c, updateText).Grow(1).FontSize(11.5).TextColor(updateColor).Wrap()

			switch {
			case vm.updateDownloading:
				if ui.Button(c.Key("btn-cancel-update"), "取消").Padding(5, 11).Radius(7).Clicked() {
					vm.cancelUpdateDownload()
				}
			case vm.updateInstalled:
				if hasNewer && vm.updateCanReinstall {
					if ui.Button(c.Key("btn-install-supersede"), "更新至最新版").Padding(5, 10).Radius(7).Clicked() {
						vm.installNativeUpdate()
					}
				}
				if ui.PrimaryButton(c.Key("btn-restart-update"), "立即重启").Padding(5, 11).Radius(7).Clicked() {
					vm.restartForUpdate()
				}
			case vm.updateNativeEnabled && vm.updateAvailable != "":
				if ui.PrimaryButton(c.Key("btn-install-update"), "立即更新").Padding(5, 11).Radius(7).Clicked() {
					vm.installNativeUpdate()
				}
			case vm.updateResult != nil && vm.updateResult.HasUpdate && vm.updateResult.ReleaseURL != "":
				if ui.PrimaryButton(c.Key("btn-open-release"), "前往下载").Padding(5, 11).Radius(7).Clicked() {
					releaseURL := vm.updateResult.ReleaseURL
					if vm.app != nil {
						go func() { _ = mygo.Shell.OpenExternal(releaseURL) }()
					}
				}
			}
			if !vm.updateDownloading {
				if ui.Button(c.Key("btn-check-update"), "检查更新").Disabled(vm.checkingUpdate).Padding(5, 11).Radius(7).Clicked() {
					vm.checkUpdate()
				}
			}
		})

		if vm.updateDownloading {
			_, detail, pctLabel, ratio := vm.updateProgressState()
			ui.Column(c).Padding(8, 10).Gap(5).Radius(8).
				Background(p.cardElevated).Border(1, p.border).Children(func() {
				ui.Progress(c.Key("settings-update-progress"), ratio).
					Height(6).Radius(99).Background(p.pillBg).TextColor(p.accent).Label("更新下载进度")
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					if detail != "" {
						ui.Text(c, detail).Grow(1).FontSize(10.5).TextColor(p.textMuted).Ellipsis("…")
					} else {
						ui.Spacer(c)
					}
					if pctLabel != "" {
						ui.Text(c, pctLabel).FontSize(10.5).Bold().TextColor(p.accent).NoWrap()
					}
				})
			})
		}

		if notes := vm.activeUpdateNotes(); notes != "" {
			vm.renderUpdateNotesMarkdown(c, p, notes)
		}
	})
}

// updateProgressState 返回当前下载阶段的标题文案、字节详情、百分比标签与进度值（<0 表示不确定动画）。
func (vm *viewModel) updateProgressState() (title, detail, pctLabel string, ratio float64) {
	ver := strings.TrimSpace(vm.updateAvailable)
	if ver == "" {
		ver = "新版本"
	}
	switch {
	case vm.updateTotal > 0 && vm.updateDownloaded >= vm.updateTotal:
		return fmt.Sprintf("正在校验并安装 %s…", ver),
			fmt.Sprintf("已下载 %s / %s", FormatBytes(vm.updateTotal), FormatBytes(vm.updateTotal)),
			"100%",
			1.0
	case vm.updateTotal > 0:
		r := min(1.0, max(0.0, float64(vm.updateDownloaded)/float64(vm.updateTotal)))
		pct := min(100, int(vm.updateDownloaded*100/vm.updateTotal))
		return fmt.Sprintf("正在下载 %s", ver),
			fmt.Sprintf("已下载 %s / %s", FormatBytes(vm.updateDownloaded), FormatBytes(vm.updateTotal)),
			fmt.Sprintf("%d%%", pct),
			r
	case vm.updateDownloaded > 0:
		return fmt.Sprintf("正在下载 %s", ver),
			fmt.Sprintf("已下载 %s", FormatBytes(vm.updateDownloaded)),
			"",
			-1
	default:
		return fmt.Sprintf("正在准备下载 %s…", ver),
			"正在连接更新服务器…",
			"",
			-1
	}
}

// activeUpdateNotes 返回当前可用或已就绪更新的 Markdown 说明内容。
func (vm *viewModel) activeUpdateNotes() string {
	if vm.updateNativeEnabled && vm.updateAvailable != "" {
		if notes := strings.TrimSpace(vm.updateNotes); notes != "" {
			return notes
		}
	}
	if vm.updateResult != nil && vm.updateResult.HasUpdate {
		if notes := strings.TrimSpace(vm.updateResult.ReleaseNotes); notes != "" {
			return notes
		}
	}
	return ""
}

// renderUpdateNotesMarkdown 使用 MyGo 原生 Markdown 组件渲染更新日志，保持与设置卡片一致的紧凑视觉层次。
func (vm *viewModel) renderUpdateNotesMarkdown(c *ui.Context, p palette, notes string) {
	vm.updateMarkdownOpts.Theme = updateMarkdownTheme(p)
	vm.updateMarkdownOpts.WrapCode = true
	if vm.updateMarkdownOpts.OnLink == nil {
		vm.updateMarkdownOpts.OnLink = func(dest string) {
			if vm.app != nil && ui.SafeToOpen(dest) {
				go func(target string) { _ = mygo.Shell.OpenExternal(target) }(dest)
			}
		}
	}
	ui.Column(c).Padding(10, 12).Radius(9).Background(p.cardElevated).Border(1, p.border).Children(func() {
		markdown.View(c.Key("update-notes-md"), notes, &vm.updateMarkdownOpts)
	})
}

func updateMarkdownTheme(p palette) *markdown.Theme {
	th := &markdown.Theme{
		FontSize:            11.5,
		CodeFont:            "monospace",
		LineHeight:          1.5,
		CodeLineHeight:      1.38,
		Text:                p.textSecondary,
		Muted:               p.textMuted,
		Link:                p.accent,
		Strong:              p.text,
		StrongWeight:        600,
		Code:                p.text,
		CodeBackground:      p.pillBg,
		CodeSize:            0.9,
		BlockSpacing:        0.52,
		ItemSpacing:         0.22,
		ListIndent:          1.35,
		QuoteBar:            p.borderStrong,
		QuoteText:           p.textMuted,
		QuoteBarWidth:       2.5,
		Rule:                p.border,
		CodeBlockBackground: p.card,
		CodeBlockBorder:     p.border,
		CodeBlockRadius:     6,
		CodeBlockPadding:    0.65,
		TableBorder:         p.border,
		TableHeader:         p.pillBg,
		Checkbox:            p.accent,
	}
	th.Headings = [6]markdown.HeadingStyle{
		{Size: 1.18, Weight: 700, Color: p.text, Top: 0.12, Bottom: 0.35},
		{Size: 1.10, Weight: 700, Color: p.text, Top: 0.52, Bottom: 0.25},
		{Size: 1.04, Weight: 600, Color: p.text, Top: 0.48, Bottom: 0.22},
		{Size: 1.00, Weight: 600, Color: p.text, Top: 0.42, Bottom: 0.18},
		{Size: 0.96, Weight: 600, Color: p.textSecondary, Top: 0.36, Bottom: 0.15},
		{Size: 0.92, Weight: 600, Color: p.textMuted, Top: 0.32, Bottom: 0.15},
	}
	return th
}
