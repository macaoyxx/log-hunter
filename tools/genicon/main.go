// genicon 生成 LogHunter 应用图标：深色圆角底 + 日志行 + 琥珀放大镜
// 标准 SDF（有符号距离场）+ 8x 超采样抗锯齿
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

const SS = 8    // 超采样倍数
const OUT = 256 // 输出尺寸

type F = color.RGBA

var (
	amber = F{245, 165, 36, 255}
)

// ---------- SDF 基元（返回到形状边缘的有符号距离，负值在内部） ----------

func sdRoundedBox(x, y, cx, cy, hw, hh, r float64) float64 {
	dx := math.Abs(x-cx) - hw + r
	dy := math.Abs(y-cy) - hh + r
	ox, oy := math.Max(dx, 0), math.Max(dy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - r
}

func sdRing(x, y, cx, cy, r, w float64) float64 {
	return math.Abs(math.Hypot(x-cx, y-cy) - r) - w/2
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

// sdShape 返回各层在 (x,y) 的 SDF（负值在内部），层号：0 底板 / 1 日志行 / 2 高亮行 / 3 放大镜
func sdShape(x, y float64) (d [4]float64) {
	for i := range d {
		d[i] = 1e9
	}
	// 放大镜：镜片环 + 手柄（右下）
	d[3] = math.Min(
		sdRing(x, y, 172, 172, 40, 11),
		sdSegment(x, y, 200, 200, 228, 228, 13),
	)
	// 日志行：三行（短块+长块），第二行高亮
	rows := []struct{ y float64 }{{62}, {102}, {142}}
	for i, r := range rows {
		cy := r.y + 7
		short := sdRoundedBox(x, y, 74, cy, 22, 7, 3)
		long := sdRoundedBox(x, y, 163, cy, 55, 7, 3)
		v := math.Min(short, long)
		if i == 1 {
			d[2] = math.Min(d[2], v)
		} else {
			d[1] = math.Min(d[1], v)
		}
	}
	// 底板
	d[0] = sdRoundedBox(x, y, 128, 128, 102, 102, 54)
	return d
}

func main() {
	img := image.NewRGBA(image.Rect(0, 0, OUT, OUT))

	for py := 0; py < OUT; py++ {
		for px := 0; px < OUT; px++ {
			var cov [4]float64
			for sy := 0; sy < SS; sy++ {
				for sx := 0; sx < SS; sx++ {
					x := float64(px) + (float64(sx)+0.5)/SS
					y := float64(py) + (float64(sy)+0.5)/SS
					d := sdShape(x, y)
					for k := range d {
						if d[k] < 0.5 { // 只累计覆盖，省一次 clamp
							cov[k] += clamp01(0.5 - d[k])
						}
					}
				}
			}
			n := float64(SS * SS)

			// 底色：竖向微渐变（上略亮）
			g := float64(py) / float64(OUT)
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

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	pngBytes := buf.Bytes()
	wd, _ := os.Getwd()
	if err := os.WriteFile(filepath.Join(wd, "build", "appicon.png"), pngBytes, 0644); err != nil {
		panic(err)
	}

	// PNG-in-ICO（256×256：宽高字段填 0）
	ico := &bytes.Buffer{}
	ico.Write([]byte{0, 0, 1, 0, 1, 0})
	ico.Write([]byte{0, 0, 0, 0, 1, 0, 32, 0})
	var w32 [4]byte
	binary.LittleEndian.PutUint32(w32[:], uint32(len(pngBytes)))
	ico.Write(w32[:])
	binary.LittleEndian.PutUint32(w32[:], 22)
	ico.Write(w32[:])
	ico.Write(pngBytes)
	if err := os.WriteFile(filepath.Join(wd, "build", "windows", "icon.ico"), ico.Bytes(), 0644); err != nil {
		panic(err)
	}
	fmt.Println("icon 生成完成: build/appicon.png + build/windows/icon.ico")
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }
