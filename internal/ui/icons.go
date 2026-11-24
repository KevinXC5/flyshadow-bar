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
	svgBolt = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></svg>`))
	svgStarFilled = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`))
	svgStarOutline = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>`))
	svgRefresh = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><polyline points="21 3 21 9 15 9"/></svg>`))
	svgGauge = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/></svg>`))
	svgCopy = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`))
	svgPower = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>`))
)

// makeTrayIconPNG 生成 36x36 高清抗锯齿托盘图标（精致纸飞机 / 飞影羽翼造型）。
// macOS 上使用纯黑透明模板图由系统自动反色；Windows 上按连接状态着色以保持高对比度。
func makeTrayIconPNG(connected bool) []byte {
	const size = 36
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	ink := color.NRGBA{R: 15, G: 23, B: 42, A: 255}
	accent := color.NRGBA{R: 16, G: 185, B: 129, A: 255} // 翡翠绿指示点
	if runtime.GOOS == "darwin" {
		// 模板图标只需黑 + 透明通道
		ink = color.NRGBA{R: 0, G: 0, B: 0, A: 255}
		if !connected {
			ink.A = 140
		}
	} else {
		if connected {
			ink = color.NRGBA{R: 56, G: 189, B: 248, A: 255} // 晴空蓝
		} else {
			ink = color.NRGBA{R: 148, G: 163, B: 184, A: 255} // 浅石板灰
		}
	}

	// 纸飞机主三角形与尾翼切口（2x 超采样平滑抗锯齿）
	poly := [][2]float64{
		{31, 6},  // 机头
		{5, 16},  // 左翼尖
		{15, 20}, // 机身中折
		{18, 30}, // 尾翼底
		{23, 22}, // 机身右下折
		{31, 6},
	}

	const ss = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			hits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if pointInPoly(px, py, poly) {
						// 挖出机身内部折痕增强立体感
						if !pointNearSegment(px, py, 15.0, 20.0, 27.5, 9.5, 1.05) {
							hits++
						}
					}
				}
			}
			if hits > 0 {
				a := uint8(math.Round(float64(ink.A) * float64(hits) / float64(ss*ss)))
				img.SetNRGBA(x, y, color.NRGBA{R: ink.R, G: ink.G, B: ink.B, A: a})
			}
		}
	}

	// Windows 非模板图标在右下角加连接状态绿点
	if runtime.GOOS != "darwin" && connected {
		cx, cy, rad := 28.5, 28.5, 4.2
		for y := 22; y < size; y++ {
			for x := 22; x < size; x++ {
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
