package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"

	"github.com/egoist/mygo/ui"
)

// 界面内置矢量 SVG 图标（线条克制简洁，统一 16x16 坐标系）。
var (
	svgBolt        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></svg>`))
	svgStarFilled  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`))
	svgStarOutline = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`))
	svgRefresh     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><polyline points="21 3 21 9 15 9"/></svg>`))
	svgGauge       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/></svg>`))
	svgCopy        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`))
	svgPower       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>`))
)

// makeTrayIconPNG 生成 32x32 高清抗锯齿托盘图标（与 App Logo 呼应的双层几何飞翼造型）。
// macOS 上使用纯黑透明模板图由系统自动反色适配深浅菜单栏；
// Windows 上使用高对比度晴空蓝双翼 + 翡翠绿连接点，深浅任务栏均清晰醒目。
func makeTrayIconPNG(connected bool) []byte {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	primary := color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	secondary := color.NRGBA{R: 0, G: 0, B: 0, A: 170}
	accent := color.NRGBA{R: 16, G: 185, B: 129, A: 255} // 翡翠绿指示点

	if runtime.GOOS == "darwin" {
		if !connected {
			primary.A = 145
			secondary.A = 95
		}
	} else {
		if connected {
			primary = color.NRGBA{R: 56, G: 189, B: 248, A: 255}   // 冰青电光蓝
			secondary = color.NRGBA{R: 99, G: 102, B: 241, A: 235} // 靛蓝影翼
		} else {
			primary = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
			secondary = color.NRGBA{R: 100, G: 116, B: 139, A: 200}
		}
	}

	// 前层主飞翼（饱满足够醒目，居中占满 32x32 视觉安全区）
	frontWing := [][2]float64{
		{28.5, 4.0},  // 主机头
		{3.5, 13.5},  // 左翼尖
		{12.5, 16.5}, // 中折点
		{11.5, 27.0}, // 下尾尖
		{18.5, 18.5}, // 右内折
		{28.5, 4.0},
	}
	// 后层影翼（右下错位叠层，呼应 Logo 双翼层次）
	shadowWing := [][2]float64{
		{28.5, 11.5}, // 影翼尖
		{17.5, 17.5}, // 中连接
		{16.0, 28.0}, // 影尾尖
		{28.5, 11.5},
	}

	const ss = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			frontHits := 0
			shadowHits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					inFront := pointInPoly(px, py, frontWing) &&
						!pointNearSegment(px, py, 12.2, 16.3, 25.0, 7.2, 0.95)
					if inFront {
						frontHits++
					} else if pointInPoly(px, py, shadowWing) &&
						!pointNearSegment(px, py, 18.5, 18.5, 11.5, 27.0, 1.35) {
						shadowHits++
					}
				}
			}
			total := frontHits + shadowHits
			if total == 0 {
				continue
			}
			if frontHits >= shadowHits {
				a := uint8(math.Round(float64(primary.A) * float64(total) / float64(ss*ss)))
				img.SetNRGBA(x, y, color.NRGBA{R: primary.R, G: primary.G, B: primary.B, A: a})
			} else {
				a := uint8(math.Round(float64(secondary.A) * float64(total) / float64(ss*ss)))
				img.SetNRGBA(x, y, color.NRGBA{R: secondary.R, G: secondary.G, B: secondary.B, A: a})
			}
		}
	}

	// Windows 非模板图标在右下角加连接状态绿点
	if runtime.GOOS != "darwin" && connected {
		cx, cy, rad := 26.0, 26.0, 3.8
		for y := 20; y < size; y++ {
			for x := 20; x < size; x++ {
				d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
				if d <= rad+0.8 {
					alpha := 1.0
					if d > rad-0.5 {
						alpha = max(0, (rad+0.8-d)/1.3)
					}
					if alpha > 0 {
						img.SetNRGBA(x, y, color.NRGBA{
							R: accent.R,
							G: accent.G,
							B: accent.B,
							A: uint8(math.Round(255 * alpha)),
						})
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
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
