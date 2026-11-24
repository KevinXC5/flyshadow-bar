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
)

// makeTrayIconPNG 生成当前平台的高清抗锯齿托盘图标（与 App Logo 呼应的双层几何飞翼造型）。
func makeTrayIconPNG(connected bool) []byte {
	return makeTrayIconPNGForOS(connected, runtime.GOOS)
}

// makeTrayIconPNGForOS 按目标平台生成托盘图标：
// - macOS（darwin）：36x36（18pt @2x Retina）严格纯黑透明模板图（R=0,G=0,B=0，零彩色像素），
//   配合 IconIsTemplate=true 由系统自动按浅/深菜单栏渲染为纯黑或纯白色；
// - Windows 等平台：使用高对比度冰青蓝双翼 + 翡翠绿连接状态点，深浅任务栏均清晰醒目。
func makeTrayIconPNGForOS(connected bool, goos string) []byte {
	const size = 36
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	// 默认采用纯黑单色模板像素（macOS 严禁使用原生彩色图标）
	primary := color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	secondary := color.NRGBA{R: 0, G: 0, B: 0, A: 165}
	accent := color.NRGBA{R: 16, G: 185, B: 129, A: 255} // 仅非 macOS 平台使用的翡翠绿状态点

	if goos == "darwin" {
		if !connected {
			primary.A = 140
			secondary.A = 88
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

	// 前层主飞翼（按 36x36 Retina 菜单栏安全区精修，与 App Logo 双翼轮廓一致）
	frontWing := [][2]float64{
		{32.0, 4.5},  // 主机头
		{4.0, 15.2},  // 左翼尖
		{14.1, 18.6}, // 中折点
		{13.0, 30.4}, // 下尾尖
		{20.8, 20.8}, // 右内折
		{32.0, 4.5},
	}
	// 后层影翼（右下错位叠层，保留清晰缝隙勾勒立体双翼层次）
	shadowWing := [][2]float64{
		{32.0, 12.9}, // 影翼尖
		{19.7, 19.7}, // 中连接
		{18.0, 31.5}, // 影尾尖
		{32.0, 12.9},
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
						!pointNearSegment(px, py, 13.7, 18.3, 28.2, 8.1, 1.05)
					if inFront {
						frontHits++
					} else if pointInPoly(px, py, shadowWing) &&
						!pointNearSegment(px, py, 20.8, 20.8, 13.0, 30.4, 1.50) {
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
	if goos != "darwin" && connected {
		cx, cy, rad := 29.2, 29.2, 4.2
		for y := 22; y < size; y++ {
			for x := 22; x < size; x++ {
				d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
				if d <= rad+0.9 {
					alpha := 1.0
					if d > rad-0.5 {
						alpha = max(0, (rad+0.9-d)/1.4)
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
