package ui

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
	"sync"

	"github.com/egoist/mygo/ui"
)

//go:embed assets/logo.png
var appLogoPNG []byte

var (
	appLogoOnce sync.Once
	appLogoBmp  *ui.Bitmap
)

// appLogoBitmap 懒解码内嵌的应用 Logo 位图（全生命周期仅创建一次，避免每帧重复上传 GPU 纹理）。
func appLogoBitmap() *ui.Bitmap {
	appLogoOnce.Do(func() {
		if len(appLogoPNG) == 0 {
			return
		}
		if bmp, err := ui.DecodeBitmap(appLogoPNG); err == nil {
			appLogoBmp = bmp
		}
	})
	return appLogoBmp
}

// 界面内置矢量 SVG 图标（线条克制简洁，统一 16x16 坐标系）。
var (
	svgBolt        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></svg>`))
	svgStarFilled  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`))
	svgStarOutline = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`))
	svgRefresh     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><polyline points="21 3 21 9 15 9"/></svg>`))
	svgGauge       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/></svg>`))
	svgCopy        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`))
	svgPower       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>`))
	svgEdit        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4Z"/></svg>`))
	svgTrash       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/></svg>`))
	svgClose       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>`))
	svgSortAsc     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8 4-4 4 4"/><path d="M7 4v16"/><path d="M11 12h4"/><path d="M11 16h7"/><path d="M11 20h10"/></svg>`))
	svgSortDesc    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m3 16 4 4 4-4"/><path d="M7 20V4"/><path d="M11 4h4"/><path d="M11 8h7"/><path d="M11 12h10"/></svg>`))
	svgPlus        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>`))
	svgCheck       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>`))
	svgPause       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/></svg>`))
	svgPlay        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"><polygon points="6 4 20 12 6 20 6 4"/></svg>`))
	svgKey         = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="4.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/></svg>`))
	svgEye         = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>`))
	svgEyeOff      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.88 9.88a3 3 0 1 0 4.24 4.24"/><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c6.4 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68"/><path d="M6.61 6.61A13.526 13.526 0 0 0 2 12s3.6 7 10 7a9.74 9.74 0 0 0 5.39-1.61"/><line x1="2" y1="2" x2="22" y2="22"/></svg>`))
	svgLayers      = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 12 12 17 22 12"/><polyline points="2 17 12 22 22 17"/></svg>`))
)

// makeTrayIconPNG 生成当前平台的高清抗锯齿托盘图标（与 App Logo 呼应的双层几何飞翼造型）。
func makeTrayIconPNG(connected bool) []byte {
	return makeTrayIconPNGForOS(connected, runtime.GOOS)
}

// makeTrayIconPNGForOS 生成跨平台统一的极简单色模板托盘图标（宽尾实心双翼 = 已连接，同轮廓空心描边 = 未连接）：
//   - 36x36（18pt @2x Retina）严格纯黑透明模板图（R=0,G=0,B=0，零彩色像素）；
//   - 配合 IconIsTemplate=true，macOS 菜单栏与 Windows 任务栏均自动随浅/深背景渲染为纯黑或纯白色。
func makeTrayIconPNGForOS(connected bool, goos string) []byte {
	img := renderTrayIconImage(connected, goos)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func renderTrayIconImage(connected bool, goos string) *image.NRGBA {
	const size = 36
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	if !connected {
		renderTrayDisconnectedIcon(img, size, ss, goos)
		return img
	}

	// 已连接：双端统一的极简单色饱满宽尾实心双翼（RGB=0 模板像素，由系统/mygo 自动按深浅栏着白或着黑）
	primary := color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	secondary := color.NRGBA{R: 0, G: 0, B: 0, A: 235}

	// 前层主飞翼（加宽下尾翼并扩大安全区占比，18pt/16px 下轮廓挺拔饱满）
	frontWing := [][2]float64{
		{33.0, 3.5},  // 主机头
		{2.8, 14.5},  // 左翼尖
		{13.4, 19.6}, // 中折点
		{14.8, 32.8}, // 下尾尖
		{23.8, 22.8}, // 右宽尾内折
		{33.0, 3.5},
	}
	// 后层影翼（顺着加宽尾翼在右下错位叠层，保留清晰窄缝勾勒立体层次）
	shadowWing := [][2]float64{
		{33.0, 11.2}, // 影翼尖
		{22.6, 21.4}, // 中连接
		{20.8, 33.0}, // 影尾尖
		{33.0, 11.2},
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			frontHits := 0
			shadowHits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/float64(ss)
					py := float64(y) + (float64(sy)+0.5)/float64(ss)
					inFront := pointInPoly(px, py, frontWing) &&
						!pointNearSegment(px, py, 13.4, 19.6, 27.6, 8.4, 0.85)
					if inFront {
						frontHits++
					} else if pointInPoly(px, py, shadowWing) &&
						!pointNearSegment(px, py, 23.8, 22.8, 14.8, 32.8, 1.15) {
						shadowHits++
					}
				}
			}
			total := frontHits + shadowHits
			if total == 0 {
				continue
			}
			chosen := secondary
			if frontHits >= shadowHits {
				chosen = primary
			}
			a := uint8(math.Round(float64(chosen.A) * float64(total) / float64(ss*ss)))
			img.SetNRGBA(x, y, color.NRGBA{R: chosen.R, G: chosen.G, B: chosen.B, A: a})
		}
	}

	return img
}

// renderTrayDisconnectedIcon 绘制未连接状态的纯线条宽尾空心飞翼（极简单色模板图，内部镂空充裕，与实心已连接一眼区分）。
func renderTrayDisconnectedIcon(img *image.NRGBA, size, ss int, goos string) {
	outerColor := color.NRGBA{R: 0, G: 0, B: 0, A: 245}
	innerColor := color.NRGBA{R: 0, G: 0, B: 0, A: 190}
	outerHalfW := 1.35
	innerHalfW := 0.92
	if goos != "darwin" {
		// Windows 16px 小尺寸缩放下略微加粗描边，保持极简线条利落不断裂
		outerHalfW = 1.45
		innerHalfW = 1.00
	}

	outline := [][2]float64{
		{32.4, 4.0},  // 机头
		{3.6, 14.6},  // 左翼尖
		{13.6, 19.6}, // 中折点
		{15.0, 32.0}, // 下尾尖
		{23.8, 22.6}, // 右宽尾内折
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			outerHits := 0
			innerHits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/float64(ss)
					py := float64(y) + (float64(sy)+0.5)/float64(ss)
					onOuter := false
					for i := 0; i < len(outline); i++ {
						p1 := outline[i]
						p2 := outline[(i+1)%len(outline)]
						if pointNearSegment(px, py, p1[0], p1[1], p2[0], p2[1], outerHalfW) {
							onOuter = true
							break
						}
					}
					if onOuter {
						outerHits++
					} else if pointNearSegment(px, py, 13.6, 19.6, 31.4, 4.8, innerHalfW) {
						innerHits++
					}
				}
			}
			total := outerHits + innerHits
			if total == 0 {
				continue
			}
			chosen := innerColor
			if outerHits >= innerHits {
				chosen = outerColor
			}
			a := uint8(math.Round(float64(chosen.A) * float64(total) / float64(ss*ss)))
			img.SetNRGBA(x, y, color.NRGBA{R: chosen.R, G: chosen.G, B: chosen.B, A: a})
		}
	}
}

var (
	darwinTrayBaseOnce  sync.Once
	darwinTrayBaseConn  *image.NRGBA
	darwinTrayBaseIdle  *image.NRGBA
	traySpeedRenderLock sync.Mutex
	traySpeedTester     *ui.Tester
	traySpeedUpText     string
	traySpeedDownText   string
)

// makeDarwinTraySpeedIconPNG 生成 macOS 菜单栏专用的双行紧凑网速模板图（@2x Retina，18pt 高）：
// 左侧为纯黑双翼飞标，右侧为上下两行紧凑速率（上行 ↑、下行 ↓，箭头左对齐、数值右对齐），
// 全图严格仅含纯黑透明像素（RGB=0），供 NSStatusItem template 模式随浅/深菜单栏自动切换黑白色。
func makeDarwinTraySpeedIconPNG(connected bool, upBps, downBps int64) []byte {
	darwinTrayBaseOnce.Do(func() {
		darwinTrayBaseConn = renderTrayIconImage(true, "darwin")
		darwinTrayBaseIdle = renderTrayIconImage(false, "darwin")
	})
	baseIcon := darwinTrayBaseIdle
	if connected {
		baseIcon = darwinTrayBaseConn
	}

	const (
		iconW    = 36 // 18pt @2x
		iconH    = 36 // 18pt @2x
		textDipW = 36 // 紧凑双行文字区宽度（pt），整体约 54pt 宽
		textDipH = 18 // 菜单栏图标高度（pt）
		scale    = 2
		textPxW  = textDipW * scale
		totalPxW = iconW + textPxW
		totalPxH = iconH
	)

	upVal := FormatTraySpeed(upBps)
	downVal := FormatTraySpeed(downBps)

	traySpeedRenderLock.Lock()
	traySpeedUpText = upVal
	traySpeedDownText = downVal
	if traySpeedTester == nil {
		traySpeedTester = ui.NewTester(func(c *ui.Context) {
			white := ui.Hex("#ffffff")
			black := ui.Hex("#000000")
			ui.Column(c).Size(textDipW, textDipH).Background(white).
				Justify(ui.Center).AlignItems(ui.Stretch).Gap(0).Padding(0, 1, 0, 1).Children(func() {
				ui.Row(c).Height(8.5).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "↑").FontSize(7.5).Bold().LineHeight(8.5).TextColor(black).NoWrap()
					ui.Spacer(c)
					ui.Text(c, traySpeedUpText).FontSize(8.2).Bold().LineHeight(8.5).TextColor(black).NoWrap()
				})
				ui.Row(c).Height(8.5).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "↓").FontSize(7.5).Bold().LineHeight(8.5).TextColor(black).NoWrap()
					ui.Spacer(c)
					ui.Text(c, traySpeedDownText).FontSize(8.2).Bold().LineHeight(8.5).TextColor(black).NoWrap()
				})
			})
		}, textDipW, textDipH)
		traySpeedTester.SetScale(scale)
	} else {
		traySpeedTester.Frame()
	}
	textImg := traySpeedTester.Image()
	traySpeedRenderLock.Unlock()

	out := image.NewNRGBA(image.Rect(0, 0, totalPxW, totalPxH))
	// 拷贝左侧双翼模板图标
	for y := 0; y < iconH; y++ {
		for x := 0; x < iconW; x++ {
			out.SetNRGBA(x, y, baseIcon.NRGBAAt(x, y))
		}
	}
	// 将白底黑字抗锯齿结果转为严格纯黑透明模板像素（R=0, G=0, B=0, A=255-gray）
	tb := textImg.Bounds()
	for y := 0; y < totalPxH && y < tb.Dy(); y++ {
		for x := 0; x < textPxW && x < tb.Dx(); x++ {
			c := textImg.RGBAAt(tb.Min.X+x, tb.Min.Y+y)
			// 灰度越小表示前景越黑，反相作为模板 Alpha；过滤极浅底噪
			gray := (int(c.R)*299 + int(c.G)*587 + int(c.B)*114 + 500) / 1000
			alpha := 255 - gray
			if alpha < 12 {
				continue
			}
			if alpha > 255 {
				alpha = 255
			}
			out.SetNRGBA(iconW+x, y, color.NRGBA{R: 0, G: 0, B: 0, A: uint8(alpha)})
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

func pointInPoly(x, y float64, pts [][2]float64) bool {
	inside := false
	n := len(pts)
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := pts[i][0], pts[i][1]
		xj, yj := pts[j][0], pts[j][1]
		if ((yi > y) != (yj > y)) && (x < (xj-xi)*(y-yi)/(yj-yi+1e-12)+xi) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func pointNearSegment(px, py, x1, y1, x2, y2, dist float64) bool {
	dx, dy := x2-x1, y2-y1
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-x1, py-y1) <= dist
	}
	t := ((px-x1)*dx + (py-y1)*dy) / l2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	projX := x1 + t*dx
	projY := y1 + t*dy
	return math.Hypot(px-projX, py-projY) <= dist
}
