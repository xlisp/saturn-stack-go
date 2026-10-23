package main

import (
	"fmt"
	"math"
)

type Params struct {
	BodySat, RingSat     float64
	BodyGains, RingGains []float64 // à trous 小波各层增益（层 1 最细）
	Denoise              float64
	BodyGamma, RingGamma float64
	RingContrast         float64
	WBMix                float64
	Size                 int
	Rotate               float64 // 输出旋转角（度）
	Margin               float64
}

// atrous 对单通道做 B3 样条 à trous 小波分解后按层增益重建，细层做软阈值降噪。
func atrous(L []float64, w, h int, gains []float64, denoise float64, mask []float64) []float64 {
	k := []float64{1.0 / 16, 4.0 / 16, 6.0 / 16, 4.0 / 16, 1.0 / 16}
	cur := L
	out := make([]float64, len(L))
	for j, g := range gains {
		next := sepConv(cur, w, h, k, 1<<j)
		det := make([]float64, len(L))
		var dev []float64
		for i := range det {
			det[i] = cur[i] - next[i]
			if mask[i] > 0.5 {
				dev = append(dev, math.Abs(det[i]))
			}
		}
		thr := 0.0
		if j < 2 {
			thr = denoise * percentile(dev, 50) / 0.6745 / float64(j+1)
		}
		for i := range det {
			d := det[i]
			switch {
			case d > thr:
				d -= thr
			case d < -thr:
				d += thr
			default:
				d = 0
			}
			out[i] += g * d
		}
		cur = next
	}
	for i := range out {
		out[i] += cur[i]
	}
	return out
}

func postProcess(S *Img, g Geom, p Params) (*Img, Masks) {
	w, h := S.W, S.H
	im := S.Clone()
	mk := g.masks(w, h, 1.5)
	outside := gaussBlur(mk.Planet, w, h, 6)
	fade := gaussBlur(mk.Planet, w, h, 10)

	// 1. 背景扣除
	for c := range im.P {
		var bgv []float64
		for i, v := range im.P[c] {
			if outside[i] < 0.001 {
				bgv = append(bgv, v)
			}
		}
		bg := percentile(bgv, 50)
		for i := range im.P[c] {
			im.P[c][i] -= bg
		}
	}

	// 2. 大气色散校正：R、B 通道对齐到 G
	for _, c := range []int{0, 2} {
		dx, dy := phaseCorr(im.P[1], im.P[c], w, h)
		if math.Hypot(dx, dy) > 4 {
			dx, dy = 0, 0
		}
		fmt.Printf("  色散校正 通道%d: (%.2f, %.2f) px\n", c, dx, dy)
		src := append([]float64(nil), im.P[c]...)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				im.P[c][y*w+x] = sampleCubic(src, w, h, float64(x)-dx, float64(y)-dy)
			}
		}
	}

	// 3. 以环为中性参考做白平衡
	var sum [3]float64
	n := 0.0
	L := im.Lum()
	lmax := percentile(L, 99.9)
	for i := range L {
		if mk.Ring[i] > 0.9 && L[i] > 0.3*lmax {
			for c := 0; c < 3; c++ {
				sum[c] += im.P[c][i]
			}
			n++
		}
	}
	if n > 0 {
		for c := 0; c < 3; c++ {
			gain := 1 + p.WBMix*(sum[1]/sum[c]-1)
			for i := range im.P[c] {
				im.P[c][i] *= gain
			}
		}
	}

	// 4. 亮度归一
	L = im.Lum()
	var pl []float64
	for i, v := range L {
		if mk.Planet[i] > 0.5 {
			pl = append(pl, v)
		}
	}
	norm := 0.92 / percentile(pl, 99.8)
	for c := range im.P {
		for i := range im.P[c] {
			im.P[c][i] *= norm
		}
	}
	L = im.Lum()

	// 5. 小波锐化：本体与环用不同的增益，按掩膜融合
	Lb := atrous(L, w, h, p.BodyGains, p.Denoise, mk.Planet)
	Lr := atrous(L, w, h, p.RingGains, p.Denoise, mk.Planet)

	// 6. 色度降噪 + 分区饱和度 + 分区影调
	var chroma [3][]float64
	for c := 0; c < 3; c++ {
		d := make([]float64, w*h)
		for i := range d {
			d[i] = im.P[c][i] - L[i]
		}
		chroma[c] = gaussBlur(d, w, h, 1.2)
	}
	out := NewImg(w, h)
	for i := range L {
		mb, mr := mk.Body[i], mk.Ring[i]
		rest := math.Max(0, 1-mb-mr)
		ls := mb*Lb[i] + mr*Lr[i] + rest*(Lb[i]+Lr[i])/2
		ls = math.Max(0, ls)
		tb := math.Pow(ls, p.BodyGamma)
		tr := math.Pow(ls, p.RingGamma)
		tr = clamp01(0.5 + (tr-0.5)*p.RingContrast)
		lt := mb*tb + mr*tr + rest*ls
		sat := mb*p.BodySat + mr*p.RingSat + rest*1.0
		ratio := 1.0
		if L[i] > 1e-4 {
			ratio = lt / math.Max(L[i], 1e-4)
		}
		ratio = math.Min(ratio, 4)
		for c := 0; c < 3; c++ {
			out.P[c][i] = lt + sat*chroma[c][i]*ratio
		}
	}
	// 去掉背景残余
	for c := range out.P {
		for i := range out.P[c] {
			v := (out.P[c][i] - 0.01) / 0.99
			out.P[c][i] = clamp01(v) * smoothstep(0.002, 0.05, fade[i])
		}
	}
	return out, mk
}

// framing 以土星中心为基准裁成正方形、放大并可选旋转。
func framing(im *Img, g Geom, p Params) *Img {
	N := p.Size
	side := 2 * g.Re * ringAOuter * p.Margin
	scale := side / float64(N)
	out := NewImg(N, N)
	rot := p.Rotate * math.Pi / 180
	c, s := math.Cos(rot), math.Sin(rot)
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			dx := (float64(x) - float64(N-1)/2) * scale
			dy := (float64(y) - float64(N-1)/2) * scale
			sx := g.Cx + dx*c + dy*s
			sy := g.Cy - dx*s + dy*c
			for ch := 0; ch < 3; ch++ {
				if sx < 0 || sy < 0 || sx > float64(im.W-1) || sy > float64(im.H-1) {
					continue
				}
				out.P[ch][y*N+x] = clamp01(sampleCubic(im.P[ch], im.W, im.H, sx, sy))
			}
		}
	}
	return out
}

// maskPreview 调试图：本体染红、环染蓝。
func maskPreview(S *Img, mk Masks) *Img {
	o := S.Clone()
	L := S.Lum()
	mx := percentile(L, 99.9)
	for i := range L {
		l := clamp01(L[i] / mx)
		o.P[0][i] = clamp01(l*0.6 + 0.4*mk.Body[i])
		o.P[1][i] = l * 0.6
		o.P[2][i] = clamp01(l*0.6 + 0.4*mk.Ring[i])
	}
	return o
}
