package main

import (
	"math"
	"sort"
)

// 土星物理常数（以赤道半径 Re 为单位）
const (
	flattening = 0.09796 // 1 - Rp/Re
	ringAOuter = 2.2694  // A 环外缘 136775 km / 60268 km
	ringCInner = 1.2390  // C 环内缘
	ringBInner = 1.5270  // B 环内缘（C 环外缘）
)

// Geom 描述画布上土星的投影几何。
// u 轴沿环的长轴 (cosθ, sinθ)，v 轴垂直 (-sinθ, cosθ)。
// 极轴投影指向 +v，B 为地球相对环面的纬度（带符号），环的近侧半边位于 v·B < 0 处。
type Geom struct {
	Cx, Cy float64
	Theta  float64
	Re     float64 // 像素
	B      float64 // 弧度，带符号
}

func (g *Geom) uv(x, y float64) (float64, float64) {
	dx, dy := x-g.Cx, y-g.Cy
	c, s := math.Cos(g.Theta), math.Sin(g.Theta)
	return dx*c + dy*s, -dx*s + dy*c
}

func (g *Geom) xy(u, v float64) (float64, float64) {
	c, s := math.Cos(g.Theta), math.Sin(g.Theta)
	return g.Cx + u*c - v*s, g.Cy + u*s + v*c
}

// Rb 是球体投影后的视极半径。
func (g *Geom) Rb() float64 {
	sb, cb := math.Sin(g.B), math.Cos(g.B)
	return g.Re * math.Sqrt(sb*sb+(1-flattening)*(1-flattening)*cb*cb)
}

// globeRho <1 表示在球面圆盘内。
func (g *Geom) globeRho(x, y float64) float64 {
	u, v := g.uv(x, y)
	return math.Hypot(u/g.Re, v/g.Rb())
}

// ringR 返回像素对应的环平面半径（Re 单位）以及是否属于环的近侧半边。
func (g *Geom) ringR(x, y float64) (float64, bool) {
	u, v := g.uv(x, y)
	sb := math.Max(math.Abs(math.Sin(g.B)), 1e-3)
	return math.Hypot(u, v/sb) / g.Re, v*g.B < 0
}

// Masks 软掩膜：本体（不含挡在前面的环）、环（含越过本体的前半段）。
// CRing 是环脊上 C 环（纱环）那一圈，是 Ring 的子集，单独做暗部保护与提亮。
type Masks struct {
	Body, Ring, Planet []float64
	CRing              []float64
	Derot              []float64 // 消自转作用区：本体去掉赤道环影带（环影不随行星自转）
}

func (g *Geom) masks(w, h int, feather float64) Masks {
	body := make([]float64, w*h)
	ring := make([]float64, w*h)
	derot := make([]float64, w*h)
	cring := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			fx, fy := float64(x), float64(y)
			inGlobe := g.globeRho(fx, fy) < 1.0
			r, front := g.ringR(fx, fy)
			inRing := r > ringCInner && r < ringAOuter*1.02
			switch {
			case inRing && (front || !inGlobe):
				ring[i] = 1
				if !inGlobe && r < ringBInner {
					cring[i] = 1
				}
			case inGlobe:
				body[i] = 1
				if r > 2.6 {
					derot[i] = 1
				}
			}
		}
	}
	m := Masks{Body: gaussBlur(body, w, h, feather), Ring: gaussBlur(ring, w, h, feather),
		Derot: gaussBlur(derot, w, h, 3*feather), CRing: gaussBlur(cring, w, h, feather)}
	m.Planet = make([]float64, w*h)
	for i := range m.Planet {
		m.Planet[i] = math.Min(1, m.Body[i]+m.Ring[i])
	}
	return m
}

// derot 把参考时刻画布上的点 (x,y) 映射到相对参考时刻 dt 小时后的位置（行星自转 phi 弧度）。
// 球体按视扁率拉伸成正球处理（WinJUPOS 同样的近似）。ok=false 表示该点转到了背面附近。
func (g *Geom) derot(x, y, phi float64) (float64, float64, bool) {
	u, v := g.uv(x, y)
	rb := g.Rb()
	px, py := u/g.Re, v/rb
	rr := px*px + py*py
	if rr >= 1 {
		return x, y, true
	}
	pz := math.Sqrt(1 - rr)
	// 极轴单位向量（观察坐标：x→u, y→v, z→观察者）
	ax, ay, az := 0.0, math.Cos(g.B), math.Sin(g.B)
	c, s := math.Cos(phi), math.Sin(phi)
	dot := ax*px + ay*py + az*pz
	cx, cy, cz := ay*pz-az*py, az*px-ax*pz, ax*py-ay*px
	qx := px*c + cx*s + ax*dot*(1-c)
	qy := py*c + cy*s + ay*dot*(1-c)
	qz := pz*c + cz*s + az*dot*(1-c)
	ox, oy := g.xy(qx*g.Re, qy*rb)
	return ox, oy, qz > 0.15
}

// estimateGeom 在已对齐叠加的亮度图上测量中心、环长轴方向、Re 和 |B|。
func estimateGeom(lum []float64, w, h int) Geom {
	L := cleanLum(lum, w, h)
	cx, cy := symCenter(L, w, h)
	theta := axisAngle(L, w, h, cx, cy)
	g := Geom{Cx: cx, Cy: cy, Theta: theta}

	// 沿长轴的亮度剖面 → A 环外缘（半高处）
	prof := func(u float64) float64 {
		s := 0.0
		for dv := -2.0; dv <= 2; dv++ {
			x, y := g.xy(u, dv)
			s += sampleCubic(L, w, h, x, y)
		}
		return s / 5
	}
	maxU := math.Hypot(float64(w), float64(h)) / 2
	edge := func(sign float64) float64 {
		gmax := 0.0
		for t := 0.0; t < maxU; t += 0.5 {
			gmax = math.Max(gmax, prof(sign*t))
		}
		outer := 0.0
		for t := 0.0; t < maxU; t += 0.5 {
			if prof(sign*t) > 0.25*gmax {
				outer = t
			}
		}
		peak := 0.0
		for t := 0.7 * outer; t <= outer; t += 0.5 {
			peak = math.Max(peak, prof(sign*t))
		}
		for t := outer + 5; t > 0; t -= 0.25 {
			if prof(sign*t) >= 0.5*peak {
				return t
			}
		}
		return outer
	}
	a := (edge(1) + edge(-1)) / 2
	g.Re = a / ringAOuter

	// 在本体两侧的若干列上测环外缘的半宽 → 环椭圆短半轴 b，sinB = b/a
	var bs []float64
	for side := -1.0; side <= 1; side += 2 {
		for uu := 1.25 * g.Re; uu < 0.85*a; uu += 1 {
			u := side * uu
			colMax := 0.0
			for v := -0.6 * a; v <= 0.6*a; v += 0.5 {
				x, y := g.xy(u, v)
				colMax = math.Max(colMax, sampleCubic(L, w, h, x, y))
			}
			if colMax <= 0 {
				continue
			}
			var top, bot float64
			for v := 0.6 * a; v > 0; v -= 0.25 {
				x, y := g.xy(u, v)
				if sampleCubic(L, w, h, x, y) >= 0.5*colMax {
					top = v
					break
				}
			}
			for v := -0.6 * a; v < 0; v += 0.25 {
				x, y := g.xy(u, v)
				if sampleCubic(L, w, h, x, y) >= 0.5*colMax {
					bot = -v
					break
				}
			}
			k := math.Sqrt(1 - (u/a)*(u/a))
			bs = append(bs, (top+bot)/2/k)
		}
	}
	sort.Float64s(bs)
	b := bs[len(bs)/2]
	g.B = math.Asin(math.Min(0.99, b/a))
	return g
}

// frontScore 衡量假设近侧半边在 v·B<0 时，本体内环带沿环平面半径的结构强度。
// 真正挡在本体前面的那半边环会在 C/B/A 环边缘和卡西尼缝处出现明显跳变。
func frontScore(lum []float64, w, h int, g Geom) float64 {
	const nb = 60
	var sum [nb]float64
	var cnt [nb]float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x), float64(y)
			if g.globeRho(fx, fy) > 0.92 {
				continue
			}
			r, front := g.ringR(fx, fy)
			if !front || r < 1.0 || r > 2.5 {
				continue
			}
			k := int((r - 1.0) / 1.5 * nb)
			if k >= 0 && k < nb {
				sum[k] += lum[y*w+x]
				cnt[k]++
			}
		}
	}
	var prev float64
	tv, have := 0.0, false
	for k := 0; k < nb; k++ {
		if cnt[k] < 3 {
			continue
		}
		v := sum[k] / cnt[k]
		if have {
			tv += math.Abs(v - prev)
		}
		prev, have = v, true
	}
	return tv
}

// cleanLum 扣背景并压掉噪声底，用于几何测量。
func cleanLum(lum []float64, w, h int) []float64 {
	bg := percentile(lum, 50)
	dev := make([]float64, len(lum))
	for i, v := range lum {
		dev[i] = math.Abs(v - bg)
	}
	sigma := percentile(dev, 50) / 0.6745
	out := make([]float64, len(lum))
	for i, v := range lum {
		out[i] = math.Max(0, v-bg-4*sigma)
	}
	return out
}

// symCenter 利用土星+环的点对称性：图像与其 180° 旋转做相位相关。
func symCenter(L []float64, w, h int) (float64, float64) {
	R := make([]float64, len(L))
	for i := range L {
		R[i] = L[len(L)-1-i]
	}
	dx, dy := phaseCorr(L, R, w, h)
	return (float64(w-1) + dx) / 2, (float64(h-1) + dy) / 2
}

// axisAngle 用二阶矩求亮区长轴方向（环主导）。
func axisAngle(L []float64, w, h int, cx, cy float64) float64 {
	mx := 0.0
	for _, v := range L {
		mx = math.Max(mx, v)
	}
	var sxx, syy, sxy float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if L[y*w+x] < 0.15*mx {
				continue
			}
			dx, dy := float64(x)-cx, float64(y)-cy
			sxx += dx * dx
			syy += dy * dy
			sxy += dx * dy
		}
	}
	return 0.5 * math.Atan2(2*sxy, sxx-syy)
}
