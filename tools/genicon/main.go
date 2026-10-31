// genicon 生成 LogHunter 应用图标：深色圆角底 + 日志行 + 琥珀放大镜。
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
	amber = F{245, 165, 36, 255}
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

// sdShape 各层 SDF（层号：0 底板 / 1 日志行 / 2 高亮行 / 3 放大镜）。
// pad 为笔画加粗量（基准坐标系单位，0=原设计）——小尺寸档渲染时加粗，
// 保证 16/32px 下环与行块的笔画不低于约 1.5 物理像素，避免细线碎裂
func sdShape(x, y, pad float64) (d [4]float64) {
	for i := range d {
		d[i] = 1e9
	}
	// 放大镜：镜片环 + 手柄（右下）
	d[3] = math.Min(
		sdRing(x, y, 172, 172, 40, 11+2*pad),
		sdSegment(x, y, 200, 200, 228, 228, 13+2*pad),
	)
	// 日志行：三行（短块+长块），第二行高亮
	rows := []struct{ y float64 }{{62}, {102}, {142}}
	for i, r := range rows {
		cy := r.y + 7
		short := sdRoundedBox(x, y, 74, cy, 22, 7+pad, 3)
		long := sdRoundedBox(x, y, 163, cy, 55, 7+pad, 3)
		v := math.Min(short, long)
		if i == 1 {
			d[2] = math.Min(d[2], v)
		} else {
			d[1] = math.Min(d[1], v)
		}
	}
	// 底板（轮廓不加粗，保持外沿形状不变）
	d[0] = sdRoundedBox(x, y, 128, 128, 102, 102, 54)
	return d
}

// render 以 size×size 像素原生渲染一档：采样点映射回基准坐标系求 SDF，
// 距离乘 scale 折算成像素单位做 0.5px 抗锯齿——每档都是原生分辨率渲染，
// 不存在任何缩放重采样
func render(size int) *image.RGBA {
	scale := float64(size) / BASE
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
			var cov [4]float64
			for sy := 0; sy < SS; sy++ {
				for sx := 0; sx < SS; sx++ {
					// 像素内超采样点 → 基准坐标
					x := (float64(px) + (float64(sx)+0.5)/SS) / scale
					y := (float64(py) + (float64(sy)+0.5)/SS) / scale
					d := sdShape(x, y, pad)
					for k := range d {
						dk := d[k] * scale // 折算为像素单位距离
						if dk < 0.5 {
							cov[k] += clamp01(0.5 - dk)
						}
					}
				}
			}
			n := float64(SS * SS)

			// 底色：竖向微渐变（上略亮）
			g := float64(py) / float64(size)
			base := F{
				uint8(lerp(24, 13, g)),
				uint8(lerp(28, 15, g)),
				uint8(lerp(37, 20, g)),
				255,
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
				layers = append(layers, layer{F{120, 130, 150, 255}, f})
			}
			if f := cov[2] / n; f > 0 {
				layers = append(layers, layer{amber, f})
			}
			if f := cov[3] / n; f > 0 {
				layers = append(layers, layer{amber, f})
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
	// appicon.png：最大档（关于弹窗/前端资产）
	if err := os.WriteFile(filepath.Join(wd, "build", "appicon.png"), pngs[len(pngs)-1], 0644); err != nil {
		panic(err)
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
