// genicon 生成 LogHunter 应用图标：深色圆角底 + 错落日志行 + 冰蓝准星锁定 ERROR 红条
//（v0.3.0「猎径图」方向：三条灰蓝日志条，最短一条为 ERROR 红；准星环+四刻度+亮点压在红条上）。
// 标准 SDF（有符号距离场）+ 8x 超采样抗锯齿；ICO 内嵌 7 档尺寸（16~256）
// 每档独立原生渲染（小档自动加粗笔画防碎裂）——此前 ICO 只有单层 256 大图，
// Windows 标题栏取小尺寸时被系统低质缩放导致锯齿毛边。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const SS = 8       // 超采样倍数
const BASE = 256.0 // 图形设计基准坐标系（所有 SDF 几何按此定义）

type F = color.RGBA

var (
	ice       = F{R: 95, G: 178, B: 255, A: 255}  // 准星环/刻度（--accent）
	iceBright = F{R: 143, G: 205, B: 255, A: 255} // 中心亮点（--accent-strong）
	errRed    = F{R: 237, G: 106, B: 90, A: 255}  // ERROR 行（--danger）
)

// ---------- SDF 基元（基准坐标系，返回有符号距离，负值在内部） ----------

func sdRoundedBox(x, y, cx, cy, hw, hh, r float64) float64 {
	dx := math.Abs(x-cx) - hw + r
	dy := math.Abs(y-cy) - hh + r
	ox, oy := math.Max(dx, 0), math.Max(dy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - r
}

func sdRing(x, y, cx, cy, r, w float64) float64 {
	return math.Abs(math.Hypot(x-cx, y-cy)-r) - w/2
}

func sdSegment(x, y, x0, y0, x1, y1, w float64) float64 {
	vx, vy := x1-x0, y1-y0
	l2 := vx*vx + vy*vy
	t := 0.0
	if l2 > 0 {
		t = clamp01(((x-x0)*vx + (y-y0)*vy) / l2)
	}
	return math.Hypot(x-(x0+vx*t), y-(y0+vy*t)) - w/2
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// sdShape 各层 SDF（层号：0 底板 / 1 灰蓝日志行 / 2 ERROR 红行 / 3 准星环+刻度 / 4 中心亮点）。
// pad 为笔画加粗量（基准坐标系单位，0=原设计）——小尺寸档渲染时加粗，
// 保证 16/32px 下环与行块的笔画不低于约 1.5 物理像素，避免细线碎裂。
// lvl 为细节分级（视觉模型核验结论：小尺寸元素过多会互相粘连）：
//   0=完整设计（≥48px） 2=仅粗环+环心红条（≤32px，pad 加粗在小档会把环撑实，改显式几何）
func sdShape(x, y, pad float64, lvl int) (d [5]float64) {
	for i := range d {
		d[i] = 1e9
	}
	// lvl2（16/24px）显式简化几何：粗环 + 环心红条（准星锁定红色目标）。
	// 不走 pad 加粗——pad 折算到 16px 达 22 基准单位，会把环宽撑到填满圆心
	if lvl == 2 {
		d[3] = sdRing(x, y, 128, 128, 58, 36)
		d[2] = sdRoundedBox(x, y, 128, 128, 34, 13, 13)
		d[0] = sdRoundedBox(x, y, 128, 128, 102, 102, 54)
		return d
	}
	// 准星：环 + 四向刻度（仅 lvl0）
	d[3] = sdRing(x, y, 128, 130, 57, 7+2*pad)
	if lvl == 0 {
		for _, s := range [][4]float64{
			{128, 53, 128, 69}, {128, 191, 128, 207}, // 上/下
			{51, 130, 67, 130}, {189, 130, 205, 130}, // 左/右
		} {
			d[3] = math.Min(d[3], sdSegment(x, y, s[0], s[1], s[2], s[3], 7+2*pad))
		}
	}
	// 中心亮点：压在 ERROR 行上（锁定意象）——仅 lvl0（小尺寸与红条叠印粘连，省略）
	if lvl == 0 {
		d[4] = math.Hypot(x-128, y-158.5) - 11
	}
	// 日志行：三条错落（长/中/短=ERROR），高度 17、圆角 8.5——仅 lvl0
	if lvl == 0 {
		d[1] = math.Min(
			sdRoundedBox(x, y, 128, 100.5, 76, 8.5+pad, 8.5),
			sdRoundedBox(x, y, 106, 129.5, 54, 8.5+pad, 8.5),
		)
	}
	// 红条 hw 46：右端 140 把亮点（右缘 139）完整包进条内，防亮点悬出边缘成粘连
	d[2] = sdRoundedBox(x, y, 96, 158.5, 46, 8.5+pad, 8.5)
	// 底板（轮廓不加粗，保持外沿形状不变）
	d[0] = sdRoundedBox(x, y, 128, 128, 102, 102, 54)
	return d
}

// render 以 size×size 像素原生渲染一档：采样点映射回基准坐标系求 SDF，
// 距离乘 scale 折算成像素单位做 0.5px 抗锯齿——每档都是原生分辨率渲染，
// 不存在任何缩放重采样
func render(size int) *image.RGBA {
	scale := float64(size) / BASE
	// 细节分级：小尺寸做减法（视觉核验：元素多则粘连）——≤32 一律极简档
	lvl := 0
	if size <= 32 {
		lvl = 2
	}
	// 小尺寸笔画加粗（像素单位）：256/128 原设计；64 起逐档递增防碎裂
	padPx := 0.0
	switch {
	case size <= 16:
		padPx = 1.4
	case size <= 24:
		padPx = 1.2
	case size <= 32:
		padPx = 1.0
	case size <= 48:
		padPx = 0.7
	case size <= 64:
		padPx = 0.5
	}
	pad := padPx / scale // 折回基准坐标系

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var cov [5]float64
			for sy := 0; sy < SS; sy++ {
				for sx := 0; sx < SS; sx++ {
					// 像素内超采样点 → 基准坐标
					x := (float64(px) + (float64(sx)+0.5)/SS) / scale
					y := (float64(py) + (float64(sy)+0.5)/SS) / scale
					d := sdShape(x, y, pad, lvl)
					for k := range d {
						dk := d[k] * scale // 折算为像素单位距离
						if dk < 0.5 {
							cov[k] += clamp01(0.5 - dk)
						}
					}
				}
			}
			n := float64(SS * SS)

			// 底色：竖向微渐变（上略亮，冷蓝黑）
			g := float64(py) / float64(size)
			base := F{
				R: uint8(lerp(16, 9, g)),
				G: uint8(lerp(19, 11, g)),
				B: uint8(lerp(27, 16, g)),
				A: 255,
			}
			type layer struct {
				col F
				a   float64
			}
			var layers []layer
			if f := cov[0] / n; f > 0 {
				layers = append(layers, layer{base, f})
			}
			if f := cov[1] / n; f > 0 {
				layers = append(layers, layer{F{R: 104, G: 114, B: 138, A: 255}, f})
			}
			if f := cov[2] / n; f > 0 {
				layers = append(layers, layer{errRed, f})
			}
			if f := cov[3] / n; f > 0 {
				layers = append(layers, layer{ice, f})
			}
			if f := cov[4] / n; f > 0 {
				layers = append(layers, layer{iceBright, f})
			}

			r, g2, b, a := 0.0, 0.0, 0.0, 0.0
			for _, l := range layers {
				la := float64(l.col.A) / 255 * l.a
				r = lerp(r, float64(l.col.R), la)
				g2 = lerp(g2, float64(l.col.G), la)
				b = lerp(b, float64(l.col.B), la)
				a = lerp(a, 255, la)
			}
			img.Set(px, py, color.RGBA{uint8(r), uint8(g2), uint8(b), uint8(a)})
		}
	}
	return img
}

func main() {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}

	var pngs [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			panic(err)
		}
		pngs = append(pngs, buf.Bytes())
	}

	wd, _ := os.Getwd()
	// appicon.png：最大档——两处同源产出：build/（wails 打包用）与
	// frontend/src/assets/（关于弹窗引用）。曾只写 build 侧，关于弹窗的
	// 前端拷贝从不更新，旧图标一直残留在「关于」里
	for _, p := range []string{
		filepath.Join(wd, "build", "appicon.png"),
		filepath.Join(wd, "frontend", "src", "assets", "appicon.png"),
	} {
		if err := os.WriteFile(p, pngs[len(pngs)-1], 0644); err != nil {
			panic(err)
		}
	}

	// 多尺寸 PNG-in-ICO：每档独立原生渲染，系统按场景直接取对应层，零缩放
	ico := &bytes.Buffer{}
	ico.Write([]byte{0, 0, 1, 0, uint8(len(sizes)), 0})
	var offset uint32 = 6 + uint32(len(sizes))*16 // 目录区之后是首个图像数据
	var data bytes.Buffer
	for i, s := range sizes {
		b := uint8(s)
		if s >= 256 {
			b = 0 // 256 以 0 表示
		}
		ico.Write([]byte{b, b, 0, 0, 1, 0, 32, 0})
		var w32 [4]byte
		binary.LittleEndian.PutUint32(w32[:], uint32(len(pngs[i])))
		ico.Write(w32[:])
		binary.LittleEndian.PutUint32(w32[:], offset)
		ico.Write(w32[:])
		data.Write(pngs[i])
		offset += uint32(len(pngs[i]))
	}
	ico.Write(data.Bytes())
	if err := os.WriteFile(filepath.Join(wd, "build", "windows", "icon.ico"), ico.Bytes(), 0644); err != nil {
		panic(err)
	}
	fmt.Printf("icon 生成完成: build/appicon.png(256) + build/windows/icon.ico(%d 档独立渲染)\n", len(sizes))
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }
