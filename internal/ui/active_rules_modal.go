package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

type activeRuleMatchBucket struct {
	matching int
	label    string
	count    int
}

// activeRulesModalState 管理「生效规则浏览器」弹窗的只读数据、统计概览、缓存筛选与虚拟列表状态。
type activeRulesModalState struct {
	open         bool
	entries      []config.ActiveRuleEntry
	revision     uint64
	searchQuery  string
	sourceFilter string // ""=全部, "custom"=自定义, "subscribe"=订阅内置
	actionFilter int    // -1=全部, 0=直连, 1=拒绝, 2=代理
	matchFilter  int    // -1=全部匹配类型

	// 统计摘要（打开弹窗加载数据时一次性算好）
	customCount    int
	subscribeCount int
	proxyCount     int
	directCount    int
	rejectCount    int
	matchBuckets   []activeRuleMatchBucket

	// 过滤结果缓存，避免 60fps 每帧遍历近万条规则
	filtered        []config.ActiveRuleEntry
	cacheRevision   uint64
	cacheQuery      string
	cacheSource     string
	cacheAction     int
	cacheMatch      int
	cacheValid      bool
	listState       ui.ListState
	listRowScrollTo bool
}

func (vm *viewModel) openActiveRulesModal() {
	var entries []config.ActiveRuleEntry
	if vm.app != nil && vm.app.eng != nil {
		entries = vm.app.eng.ActiveRules()
	} else if len(vm.activeRulesModal.entries) > 0 {
		entries = vm.activeRulesModal.entries
	}
	vm.openActiveRulesModalWithEntries(entries)
}

func (vm *viewModel) openActiveRulesModalWithEntries(entries []config.ActiveRuleEntry) {
	st := &vm.activeRulesModal
	st.open = true
	st.entries = entries
	st.revision++
	st.searchQuery = ""
	st.sourceFilter = ""
	st.actionFilter = -1
	st.matchFilter = -1
	st.cacheValid = false
	st.listState = ui.ListState{}
	st.rebuildStats()
	st.ensureFiltered()
}

func (vm *viewModel) closeActiveRulesModal() {
	st := &vm.activeRulesModal
	st.open = false
	st.searchQuery = ""
	st.sourceFilter = ""
	st.actionFilter = -1
	st.matchFilter = -1
	st.filtered = nil
	st.cacheValid = false
	if vm.app != nil {
		// 运行时关闭即释放大规则切片，避免常驻内存占用
		st.entries = nil
	}
}

func (st *activeRulesModalState) rebuildStats() {
	st.customCount = 0
	st.subscribeCount = 0
	st.proxyCount = 0
	st.directCount = 0
	st.rejectCount = 0

	matchCounts := make(map[int]int, 8)
	for _, entry := range st.entries {
		if entry.Source == config.RuleSourceCustom {
			st.customCount++
		} else {
			st.subscribeCount++
		}
		switch entry.Rule.ProxyType {
		case 0:
			st.directCount++
		case 1:
			st.rejectCount++
		default:
			st.proxyCount++
		}
		matchCounts[entry.Rule.Matching]++
	}

	buckets := make([]activeRuleMatchBucket, 0, len(matchCounts))
	for m, cnt := range matchCounts {
		buckets = append(buckets, activeRuleMatchBucket{
			matching: m,
			label:    config.RuleMatchingLabel(m),
			count:    cnt,
		})
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].count != buckets[j].count {
			return buckets[i].count > buckets[j].count
		}
		return buckets[i].matching < buckets[j].matching
	})
	st.matchBuckets = buckets
}

func (st *activeRulesModalState) hasActiveFilter() bool {
	return strings.TrimSpace(st.searchQuery) != "" ||
		st.sourceFilter != "" ||
		st.actionFilter >= 0 ||
		st.matchFilter >= 0
}

func (st *activeRulesModalState) resetFilters() {
	st.searchQuery = ""
	st.sourceFilter = ""
	st.actionFilter = -1
	st.matchFilter = -1
	st.cacheValid = false
	st.listRowScrollTo = true
}

func (st *activeRulesModalState) ensureFiltered() []config.ActiveRuleEntry {
	q := strings.ToLower(strings.TrimSpace(st.searchQuery))
	if st.cacheValid &&
		st.cacheRevision == st.revision &&
		st.cacheQuery == q &&
		st.cacheSource == st.sourceFilter &&
		st.cacheAction == st.actionFilter &&
		st.cacheMatch == st.matchFilter {
		return st.filtered
	}

	if q == "" && st.sourceFilter == "" && st.actionFilter < 0 && st.matchFilter < 0 {
		st.filtered = st.entries
	} else {
		out := make([]config.ActiveRuleEntry, 0, min(len(st.entries), 256))
		for _, entry := range st.entries {
			if st.sourceFilter != "" && string(entry.Source) != st.sourceFilter {
				continue
			}
			if st.actionFilter >= 0 && entry.Rule.ProxyType != st.actionFilter {
				continue
			}
			if st.matchFilter >= 0 && entry.Rule.Matching != st.matchFilter {
				continue
			}
			if q != "" {
				r := entry.Rule
				if !strings.Contains(strings.ToLower(r.Domain), q) &&
					!strings.Contains(strings.ToLower(r.Label), q) &&
					!strings.Contains(strings.ToLower(config.RuleMatchingLabel(r.Matching)), q) &&
					!strings.Contains(strings.ToLower(config.RuleActionLabel(r.ProxyType)), q) {
					continue
				}
			}
			out = append(out, entry)
		}
		st.filtered = out
	}

	if st.cacheValid {
		// 筛选条件变化后将虚拟列表平滑复位到顶部，避免停留在旧偏移处
		st.listRowScrollTo = true
	}
	st.cacheRevision = st.revision
	st.cacheQuery = q
	st.cacheSource = st.sourceFilter
	st.cacheAction = st.actionFilter
	st.cacheMatch = st.matchFilter
	st.cacheValid = true
	return st.filtered
}

// renderActiveRulesModal 绘制结构化的「生效分流规则」查看弹窗：
// 顶部概览统计卡 + 来源/匹配类型分布筛选 + 搜索 + 虚拟滚动规则卡片列表，万级规则零卡顿。
func (vm *viewModel) renderActiveRulesModal(c *ui.Context, p palette) {
	st := &vm.activeRulesModal
	if !st.open {
		return
	}

	filtered := st.ensureFiltered()
	if st.listRowScrollTo {
		st.listRowScrollTo = false
		if len(filtered) > 0 {
			st.listState.ScrollTo(0, ui.Start)
		}
	}

	modal := ui.Modal(c.Key("active-rules-modal"), &st.open, func() {
		ui.Column(c).Width(448).Height(576).Padding(14).Gap(9).Radius(13).
			Background(p.card).Border(1, p.borderStrong).Children(func() {

			// 1. 弹窗标题栏
			ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
				ui.Box(c).Size(24, 24).Radius(7).Background(p.accentSoft).Center().Children(func() {
					ui.Icon(c, svgLayers).Size(13, 13).TextColor(p.accent)
				})
				ui.Text(c, "生效分流规则").FontSize(14).Bold().TextColor(p.text).NoWrap()
				renderMiniTag(c, fmt.Sprintf("%d 条", len(st.entries)), p.accent, p.accentSoft)
				if vm.snap.GeoIPLoaded {
					renderMiniTag(c, "GeoIP 已加载", p.success, p.successSoft)
				}
				ui.Spacer(c)
				closeBtn := ui.Box(c.Key("btn-active-rules-close")).Size(24, 24).Radius(6).Center().
					Cursor(ui.CursorPointer).Label("关闭生效规则").Tooltip("关闭")
				if closeBtn.Hovered() {
					closeBtn.Background(p.pillBg)
				}
				closeBtn.Children(func() {
					ui.Icon(c, svgClose).Size(13, 13).TextColor(p.textMuted)
				})
				if closeBtn.Clicked() {
					vm.closeActiveRulesModal()
				}
			})

			// 2. 四格可点击统计概览卡（展示各动作占比，同时作为快捷动作筛选）
			ui.Row(c).Gap(6).AlignItems(ui.Stretch).Children(func() {
				vm.renderActiveRuleStatCard(c, p, "stat-all", "全部规则", len(st.entries), -1, p.text, p.accentSoft, p.borderStrong)
				vm.renderActiveRuleStatCard(c, p, "stat-proxy", "代理", st.proxyCount, 2, p.accent, p.accentSoft, p.borderStrong)
				vm.renderActiveRuleStatCard(c, p, "stat-direct", "直连", st.directCount, 0, p.success, p.successSoft, p.success)
				vm.renderActiveRuleStatCard(c, p, "stat-reject", "拒绝", st.rejectCount, 1, p.danger, p.dangerSoft, p.danger)
			})

			// 3. 搜索框 + 来源筛选药丸
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.SearchField(c.Key("ar-search"), &st.searchQuery).Grow(1).Placeholder("搜索域名、IP/CIDR、进程或分组…")
			})

			// 来源分布筛选行
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Text(c, "来源").FontSize(10.5).TextColor(p.textMuted).NoWrap()
				vm.renderActiveRuleSourceChip(c, p, "", fmt.Sprintf("全部 (%d)", len(st.entries)))
				vm.renderActiveRuleSourceChip(c, p, string(config.RuleSourceCustom), fmt.Sprintf("自定义 (%d)", st.customCount))
				vm.renderActiveRuleSourceChip(c, p, string(config.RuleSourceSubscribe), fmt.Sprintf("订阅内置 (%d)", st.subscribeCount))
			})

			// 匹配类型分布横向筛选条（只列出当前规则中实际存在的类型）
			if len(st.matchBuckets) > 0 {
				ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
					ui.Text(c, "类型").FontSize(10.5).TextColor(p.textMuted).NoWrap()
					ui.ScrollHorizontal(c.Key("ar-match-scroll")).Grow(1).MinWidth(0).Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(4).Padding(0, 2, 2, 0).Children(func() {
							vm.renderActiveRuleMatchChip(c, p, -1, "全部")
							for _, b := range st.matchBuckets {
								vm.renderActiveRuleMatchChip(c, p, b.matching, fmt.Sprintf("%s %d", b.label, b.count))
							}
						})
					})
				})
			}

			// 4. 命中顺序与筛选摘要行
			ui.Row(c).AlignItems(ui.Center).Gap(6).Padding(0, 2).Children(func() {
				summary := fmt.Sprintf("共 %d 条 · 按命中优先级从高到低", len(st.entries))
				if st.hasActiveFilter() {
					summary = fmt.Sprintf("匹配 %d / %d 条", len(filtered), len(st.entries))
				}
				ui.Text(c, summary).FontSize(10.5).TextColor(p.textMuted).NoWrap()
				ui.Spacer(c)
				if st.hasActiveFilter() {
					resetBtn := ui.Row(c.Key("ar-reset-filter")).Padding(1, 5).Radius(4).
						Cursor(ui.CursorPointer).Label("重置筛选").Children(func() {
						ui.Text(c, "重置筛选").FontSize(10.5).Bold().TextColor(p.accent).NoWrap()
					})
					if resetBtn.Hovered() {
						resetBtn.Background(p.accentSoft)
					}
					if resetBtn.Clicked() {
						st.resetFilters()
					}
				} else {
					ui.Text(c, "自定义优先于订阅").FontSize(10.5).TextColor(p.textMuted).NoWrap()
				}
			})

			// 5. 虚拟化规则卡片列表（单屏只渲染可见行，支持万级规则流畅滚动）
			if len(filtered) == 0 {
				ui.Column(c).Grow(1).Center().Radius(10).Background(p.cardElevated).Border(1, p.border).Children(func() {
					emptyText := "暂无生效规则"
					if len(st.entries) > 0 {
						emptyText = "没有符合当前筛选条件的规则"
					}
					ui.Text(c, emptyText).FontSize(12).TextColor(p.textMuted)
				})
			} else {
				ui.List(c.Key("active-rules-list"), &st.listState, len(filtered), func(i int) {
					vm.renderActiveRuleRow(c, p, filtered[i])
				}).Grow(1).MinHeight(0).Margin(0, -10, 0, 0)
			}
		})
	})
	if modal.Dismissed() {
		vm.closeActiveRulesModal()
	}
}

func (vm *viewModel) renderActiveRuleStatCard(c *ui.Context, p palette, key, label string, count, actionCode int, valueColor, activeBg, activeBorder ui.Color) {
	st := &vm.activeRulesModal
	selected := st.actionFilter == actionCode
	card := ui.Column(c.Key(key)).Grow(1).Padding(7, 8).Gap(2).Radius(9).
		Cursor(ui.CursorPointer).Label(label)
	if selected {
		card.Background(activeBg).Border(1, activeBorder)
	} else {
		card.Background(p.cardElevated).Border(1, p.border)
		if card.Hovered() {
			card.Background(p.cardHover)
		}
	}
	card.Children(func() {
		ui.Text(c, label).FontSize(10).TextColor(p.textMuted).NoWrap()
		ui.Text(c, fmt.Sprintf("%d", count)).FontSize(14).Bold().TextColor(valueColor).NoWrap()
	})
	if card.Clicked() {
		if st.actionFilter == actionCode && actionCode >= 0 {
			st.actionFilter = -1
		} else {
			st.actionFilter = actionCode
		}
	}
}

func (vm *viewModel) renderActiveRuleSourceChip(c *ui.Context, p palette, src, label string) {
	st := &vm.activeRulesModal
	selected := st.sourceFilter == src
	chip := ui.Row(c.Key("ar-src-"+nonEmpty(src, "all"))).Padding(2.5, 8).Radius(99).
		Cursor(ui.CursorPointer).AlignItems(ui.Center).Label(label)
	if selected {
		chip.Background(p.accentSoft).Border(1, p.borderStrong)
	} else {
		chip.Background(p.cardElevated).Border(1, p.border)
		if chip.Hovered() {
			chip.Background(p.cardHover)
		}
	}
	chip.Children(func() {
		txt := ui.Text(c, label).FontSize(10.5).NoWrap()
		if selected {
			txt.Bold().TextColor(p.accent)
		} else {
			txt.TextColor(p.textSecondary)
		}
	})
	if chip.Clicked() {
		st.sourceFilter = src
	}
}

func (vm *viewModel) renderActiveRuleMatchChip(c *ui.Context, p palette, matching int, label string) {
	st := &vm.activeRulesModal
	selected := st.matchFilter == matching
	chip := ui.Row(c.Key(fmt.Sprintf("ar-mt-%d", matching))).Padding(2, 7.5).Radius(6).
		Cursor(ui.CursorPointer).AlignItems(ui.Center).Label(label)
	if selected {
		chip.Background(p.accentSoft).Border(1, p.borderStrong)
	} else {
		chip.Background(p.cardElevated).Border(1, p.border)
		if chip.Hovered() {
			chip.Background(p.cardHover)
		}
	}
	chip.Children(func() {
		txt := ui.Text(c, label).FontSize(10).NoWrap()
		if selected {
			txt.Bold().TextColor(p.accent)
		} else {
			txt.TextColor(p.textSecondary)
		}
	})
	if chip.Clicked() {
		if st.matchFilter == matching && matching >= 0 {
			st.matchFilter = -1
		} else {
			st.matchFilter = matching
		}
	}
}

func (vm *viewModel) renderActiveRuleRow(c *ui.Context, p palette, entry config.ActiveRuleEntry) {
	r := entry.Rule
	ui.Box(c).Padding(0, 10, 4, 0).Children(func() {
		row := ui.Row(c.Key(fmt.Sprintf("ar-row-%d", entry.Order))).
			Padding(7, 9).Radius(9).Background(p.cardElevated).Border(1, p.border).
			AlignItems(ui.Center).Gap(8)
		if row.Hovered() {
			row.Background(p.cardHover)
		}
		row.Children(func() {
			// 优先级序号
			ui.Box(c).MinWidth(40).AlignItems(ui.Start).Children(func() {
				ui.Text(c, fmt.Sprintf("#%d", entry.Order)).FontSize(10).TextColor(p.textMuted).NoWrap()
			})

			// 规则主值 + 结构化元标签
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Text(c, r.Domain).FontSize(12).Bold().TextColor(p.text).
					Ellipsis("…").Tooltip(r.Domain)

				ui.Row(c).AlignItems(ui.Center).Gap(4.5).Children(func() {
					if entry.Source == config.RuleSourceCustom {
						renderMiniTag(c, "自定义", p.warning, p.warningSoft)
					} else {
						renderMiniTag(c, "订阅", p.textMuted, p.pillBg)
					}
					renderMiniTag(c, config.RuleMatchingLabel(r.Matching), p.textSecondary, p.pillBg)
					if lbl := strings.TrimSpace(r.Label); lbl != "" {
						renderMiniTag(c, "#"+lbl, p.accent, p.accentSoft)
					}
					if r.DirectConnPriority > 0 {
						renderMiniTag(c, "直连优先", p.success, p.successSoft)
					}
				})
			})

			// 动作语义胶囊
			actFg, actBg := ruleActionColors(r.ProxyType, p)
			renderPill(c, config.RuleActionLabel(r.ProxyType), actFg, actBg)

			// 复制规则值按钮
			copyBtn := ui.Box(c.Key("copy")).Size(22, 22).Radius(5).Center().
				Cursor(ui.CursorPointer).Label("复制规则").Tooltip("复制规则内容")
			copyColor := p.textMuted
			if copyBtn.Hovered() {
				copyBtn.Background(p.pillBg)
				copyColor = p.text
			}
			copyBtn.Children(func() {
				ui.Icon(c, svgCopy).Size(12, 12).TextColor(copyColor)
			})
			if copyBtn.Clicked() {
				if vm.app != nil {
					mygo.Clipboard.WriteText(r.Domain)
				}
				vm.toastMsg = "已复制 " + r.Domain
			}
		})
	})
}
