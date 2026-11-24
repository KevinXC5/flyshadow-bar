package ui

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

var quickMatchTypes = []struct {
	code  int
	label string
}{
	{config.MatchDomainSuffix, "后缀"},
	{config.MatchDomainExact, "完全"},
	{config.MatchDomainKeyword, "关键词"},
	{config.MatchCIDR4, "CIDR"},
	{config.MatchProcessName, "进程"},
}

func (vm *viewModel) renderRulesView(c *ui.Context, p palette) {
	snap := vm.snap

	// 1. 顶部极速添加 / 编辑自定义规则卡片
	ui.Column(c).Padding(10, 12).Gap(8).Radius(12).Background(p.card).Border(1, p.border).Children(func() {
		// 第一行：输入框 + 标签 + 提交/取消按钮
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			inp := ui.TextInput(c.Key("rule-domain-input"), &vm.ruleDomainDraft).
				Grow(1).Placeholder("域名、IP/CIDR 或进程名")
			ui.TextInput(c.Key("rule-label-input"), &vm.ruleLabelDraft).
				Width(76).Placeholder("分组")

			submitIcon := svgPlus
			submitTip := "添加"
			if vm.editingRuleID > 0 {
				submitIcon = svgCheck
				submitTip = "保存"
			}
			submitBtn := ui.Box(c.Key("rule-submit")).Size(30, 30).Radius(8).Center().
				Background(p.accent).Cursor(ui.CursorPointer).Label(submitTip).Tooltip(submitTip)
			if submitBtn.Hovered() {
				submitBtn.Opacity(0.9)
			}
			submitBtn.Children(func() {
				ui.Icon(c, submitIcon).Size(15, 15).TextColor(ui.Hex("#ffffff"))
			})
			if submitBtn.Clicked() || inp.Submitted() {
				vm.submitRuleDraft()
			}
			if vm.editingRuleID > 0 {
				if renderToolbarIconBtn(c, p, "rule-cancel-edit", "取消", "取消编辑", svgClose, false, p.text, p.cardHover, p.border, false) {
					vm.resetRuleDraft()
				}
			}
		})

		// 第二行：匹配类型快捷选择 + 动作三选一
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				for _, mt := range quickMatchTypes {
					selected := vm.ruleMatchDraft == mt.code
					chip := ui.Row(c.Key(fmt.Sprintf("rm-%d", mt.code))).Padding(3, 7).Radius(6).
						Cursor(ui.CursorPointer).AlignItems(ui.Center)
					if selected {
						chip.Background(p.accentSoft).Border(1, p.borderStrong)
					} else {
						chip.Background(p.cardElevated).Border(1, p.border)
					}
					chip.Children(func() {
						txt := ui.Text(c, mt.label).FontSize(10.5).NoWrap()
						if selected {
							txt.Bold().TextColor(p.accent)
						} else {
							txt.TextColor(p.textSecondary)
						}
					})
					if chip.Clicked() {
						vm.ruleMatchDraft = mt.code
					}
				}
			})

			ui.Spacer(c)

			// 动作三选一（代理 / 直连 / 拒绝）
			actions := []struct {
				code  int
				label string
				fg    ui.Color
				bg    ui.Color
			}{
				{2, "代理", p.accent, p.accentSoft},
				{0, "直连", p.success, p.successSoft},
				{1, "拒绝", p.danger, p.dangerSoft},
			}
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				for _, act := range actions {
					selected := vm.ruleActionDraft == act.code
					pill := ui.Row(c.Key(fmt.Sprintf("ra-%d", act.code))).Padding(3, 8).Radius(6).
						Cursor(ui.CursorPointer).AlignItems(ui.Center)
					if selected {
						pill.Background(act.bg).Border(1, act.fg)
					} else {
						pill.Background(p.cardElevated).Border(1, p.border)
					}
					pill.Children(func() {
						txt := ui.Text(c, act.label).FontSize(10.5).NoWrap()
						if selected {
							txt.Bold().TextColor(act.fg)
						} else {
							txt.TextColor(p.textSecondary)
						}
					})
					if pill.Clicked() {
						vm.ruleActionDraft = act.code
					}
				}
			})
		})
	})

	// 2. 搜索与动作过滤栏
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.SearchField(c.Key("rule-search"), &vm.ruleSearchQuery).Grow(1).Placeholder("搜索规则…")

		filters := []struct {
			code  int
			label string
		}{
			{-1, "全部"},
			{2, "代理"},
			{0, "直连"},
			{1, "拒绝"},
		}
		for _, f := range filters {
			selected := vm.ruleFilterAction == f.code
			btn := ui.Button(c.Key(fmt.Sprintf("rf-%d", f.code)), f.label).Padding(5, 8).Radius(7)
			if selected {
				btn.Background(p.accentSoft).Border(1, p.borderStrong).TextColor(p.accent)
			}
			if btn.Clicked() {
				vm.ruleFilterAction = f.code
			}
		}

		if snap.HasCloudAuth {
			syncBtn := ui.Box(c.Key("rules-sync")).Size(30, 30).Radius(8).Center().
				Background(p.card).Border(1, p.border).Label("同步云端规则").Tooltip("同步云端规则")
			if snap.Syncing {
				syncBtn.Background(p.accentSoft).Border(1, p.borderStrong)
			} else {
				syncBtn.Cursor(ui.CursorPointer)
				if syncBtn.Hovered() {
					syncBtn.Background(p.cardHover)
				}
			}
			syncBtn.Children(func() {
				if snap.Syncing {
					ui.Spinner(c).Size(13, 13)
				} else {
					ui.Icon(c, svgRefresh).Size(14, 14).TextColor(p.textSecondary)
				}
			})
			if !snap.Syncing && syncBtn.Clicked() {
				vm.runAsync(func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					_, err := vm.app.eng.SyncCustomRules(ctx)
					return err
				}, "云端规则已同步")
			}
		}
	})

	// 3. 若有多个规则分组标签，显示分组启停药丸栏
	if len(snap.RuleLabels) > 1 {
		activeSet := make(map[string]bool, len(snap.ActiveRuleLabels))
		for _, l := range snap.ActiveRuleLabels {
			activeSet[l] = true
		}
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, "分组:").FontSize(11).TextColor(p.textMuted)
			for _, lbl := range snap.RuleLabels {
				isOn := activeSet[lbl]
				name := lbl
				if lbl == config.DefaultRuleActiveSentinel {
					name = "默认"
				}
				chip := ui.Row(c.Key("rlbl-"+lbl)).Padding(2.5, 8).Radius(99).
					Cursor(ui.CursorPointer).AlignItems(ui.Center).Tooltip("切换分组")
				if isOn {
					chip.Background(p.accentSoft).Border(1, p.borderStrong)
				} else {
					chip.Background(p.cardElevated).Border(1, p.border)
				}
				chip.Children(func() {
					txt := ui.Text(c, name).FontSize(10.5).NoWrap()
					if isOn {
						txt.Bold().TextColor(p.accent)
					} else {
						txt.TextColor(p.textMuted)
					}
				})
				if chip.Clicked() {
					targetLbl := lbl
					next := !isOn
					vm.runAsync(func() error {
						return vm.app.eng.SetRuleLabelActive(targetLbl, next)
					}, fmt.Sprintf("分组「%s」已%s", name, toggleWord(next)))
				}
			}
		})
	}

	filtered := vm.filteredRules()
	ui.Row(c).AlignItems(ui.Center).Padding(0, 2).Children(func() {
		summary := fmt.Sprintf("自定义 %d 条 · 生效 %d 条", len(snap.CustomRules), snap.RuleCount)
		if len(filtered) != len(snap.CustomRules) {
			summary = fmt.Sprintf("显示 %d / %d 条 · 生效 %d 条", len(filtered), len(snap.CustomRules), snap.RuleCount)
		}
		ui.Text(c, summary).FontSize(11).TextColor(p.textMuted)
	})

	// 4. 紧凑规则列表（右侧独立细滚动条槽道）
	ui.Scroll(c.Key("rules-scroll")).Grow(1).Margin(0, -10, 0, 0).Children(func() {
		ui.Column(c).Gap(5).Padding(0, 10, 6, 0).Children(func() {
			if len(filtered) == 0 {
				emptyText := "无匹配规则"
				if len(snap.CustomRules) == 0 {
					emptyText = "暂无自定义规则"
				}
				ui.Column(c).Padding(28, 16).Center().Children(func() {
					ui.Text(c, emptyText).FontSize(12.5).TextColor(p.textMuted)
				})
				return
			}
			for _, rule := range filtered {
				vm.renderRuleRow(c, p, rule)
			}
		})
	})
}

func (vm *viewModel) renderRuleRow(c *ui.Context, p palette, rule config.DomainRule) {
	enabled := rule.IsEnabled()
	bg := p.card
	bdr := p.border
	if vm.editingRuleID == rule.ID {
		bg = p.cardSelected
		bdr = p.borderStrong
	}

	row := ui.Row(c.Key(fmt.Sprintf("rule-%d", rule.ID))).Padding(7, 10).Radius(10).
		Background(bg).Border(1, bdr).AlignItems(ui.Center).Gap(8)
	if !enabled {
		row.Opacity(0.58)
	}

	row.Children(func() {
		swVal := enabled
		if ui.Switch(c.Key("sw"), &swVal).Changed() {
			id := rule.ID
			vm.runAsync(func() error {
				_, err := vm.app.eng.ToggleCustomRule(id)
				return err
			}, "")
		}

		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, rule.Domain).Grow(1).FontSize(12.5).Bold().TextColor(p.text).Ellipsis("…")
			})
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				renderMiniTag(c, config.RuleMatchingLabel(rule.Matching), p.textSecondary, p.pillBg)
				if strings.TrimSpace(rule.Label) != "" {
					renderMiniTag(c, "#"+rule.Label, p.accent, p.accentSoft)
				}
			})
		})

		actFg, actBg := ruleActionColors(rule.ProxyType, p)
		renderPill(c, config.RuleActionLabel(rule.ProxyType), actFg, actBg)

		editBtn := ui.Box(c.Key("edit")).Size(26, 26).Center().Radius(6).
			Cursor(ui.CursorPointer).Label("编辑").Tooltip("编辑")
		editColor := p.textMuted
		if vm.editingRuleID == rule.ID {
			editBtn.Background(p.accentSoft)
			editColor = p.accent
		} else if editBtn.Hovered() {
			editBtn.Background(p.pillBg)
			editColor = p.text
		}
		editBtn.Children(func() {
			ui.Icon(c, svgEdit).Size(13.5, 13.5).TextColor(editColor)
		})
		if editBtn.Clicked() {
			vm.editingRuleID = rule.ID
			vm.ruleDomainDraft = rule.Domain
			vm.ruleMatchDraft = rule.Matching
			vm.ruleActionDraft = rule.ProxyType
			vm.ruleLabelDraft = rule.Label
		}

		delBtn := ui.Box(c.Key("del")).Size(26, 26).Center().Radius(6).
			Cursor(ui.CursorPointer).Label("删除").Tooltip("删除")
		delColor := p.textMuted
		if delBtn.Hovered() {
			delBtn.Background(p.dangerSoft)
			delColor = p.danger
		}
		delBtn.Children(func() {
			ui.Icon(c, svgTrash).Size(13.5, 13.5).TextColor(delColor)
		})
		if delBtn.Clicked() {
			id := rule.ID
			dom := rule.Domain
			if vm.editingRuleID == id {
				vm.resetRuleDraft()
			}
			vm.runAsync(func() error {
				return vm.app.eng.DeleteCustomRule(id)
			}, "已删除 "+dom)
		}
	})
}

func (vm *viewModel) submitRuleDraft() {
	dom := strings.TrimSpace(vm.ruleDomainDraft)
	if dom == "" {
		vm.toastMsg = "请输入规则内容"
		return
	}

	// 智能自动识别 CIDR（如果用户选的默认后缀但输入了带 / 的 IP 段）
	matching := vm.ruleMatchDraft
	if matching == config.MatchDomainSuffix && strings.Contains(dom, "/") {
		if _, _, err := net.ParseCIDR(dom); err == nil {
			if strings.Contains(dom, ":") {
				matching = config.MatchCIDR6
			} else {
				matching = config.MatchCIDR4
			}
		}
	}

	rule := config.DomainRule{
		ID:        vm.editingRuleID,
		Domain:    dom,
		Matching:  matching,
		ProxyType: vm.ruleActionDraft,
		Label:     strings.TrimSpace(vm.ruleLabelDraft),
		Enable:    config.IntPtr(1),
	}

	isEdit := vm.editingRuleID > 0
	vm.resetRuleDraft()

	if isEdit {
		vm.runAsync(func() error {
			_, err := vm.app.eng.UpdateCustomRule(rule)
			return err
		}, "已更新 "+rule.Domain)
		return
	}

	vm.runAsync(func() error {
		_, err := vm.app.eng.AddCustomRule(rule)
		return err
	}, "已添加 "+rule.Domain)
}

func (vm *viewModel) resetRuleDraft() {
	vm.editingRuleID = 0
	vm.ruleDomainDraft = ""
	vm.ruleLabelDraft = ""
	vm.ruleMatchDraft = config.MatchDomainSuffix
	vm.ruleActionDraft = 2
}

func (vm *viewModel) filteredRules() []config.DomainRule {
	q := strings.ToLower(strings.TrimSpace(vm.ruleSearchQuery))
	out := make([]config.DomainRule, 0, len(vm.snap.CustomRules))
	for _, r := range vm.snap.CustomRules {
		if vm.ruleFilterAction >= 0 && r.ProxyType != vm.ruleFilterAction {
			continue
		}
		if q != "" {
			if !strings.Contains(strings.ToLower(r.Domain), q) &&
				!strings.Contains(strings.ToLower(r.Label), q) &&
				!strings.Contains(strings.ToLower(config.RuleMatchingLabel(r.Matching)), q) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func ruleActionColors(proxyType int, p palette) (ui.Color, ui.Color) {
	switch proxyType {
	case 0:
		return p.success, p.successSoft
	case 1:
		return p.danger, p.dangerSoft
	default:
		return p.accent, p.accentSoft
	}
}

func toggleWord(on bool) string {
	if on {
		return "启用"
	}
	return "停用"
}
