package main

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
)

type Frame struct {
	Name    string
	T       float64 // UT 小时（相对当天 0 点）
	Im      *Img
	Cx, Cy  float64
	Theta   float64
	Quality float64
	Weight  float64
	Sx, Sy  float64 // 相对参考帧的残余平移
}

var reTime = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})-(\d{2})(\d{2})_(\d)`)

// parseTime 解析 WinJUPOS 风格文件名 2026-10-22-1455_4 → 14:55.4
func parseTime(name string) (float64, error) {
	m := reTime.FindStringSubmatch(filepath.Base(name))
	if m == nil {
		return 0, fmt.Errorf("文件名里找不到时间 (YYYY-MM-DD-HHMM_T): %s", name)
	}
	day, _ := strconv.Atoi(m[3])
	hh, _ := strconv.Atoi(m[4])
	mm, _ := strconv.Atoi(m[5])
	tt, _ := strconv.Atoi(m[6])
	return float64(day)*24 + float64(hh) + (float64(mm)+float64(tt)/10)/60, nil
}

func loadFrame(path string) (*Frame, error) {
	t, err := parseTime(path)
	if err != nil {
		return nil, err
	}
	im, err := readTIFF(path)
	if err != nil {
		return nil, err
	}
	for c := range im.P {
		bg := percentile(im.P[c], 50)
		for i := range im.P[c] {
			im.P[c][i] -= bg
		}
	}
	f := &Frame{Name: filepath.Base(path), T: t, Im: im}
	L := cleanLum(im.Lum(), im.W, im.H)
	f.Cx, f.Cy = symCenter(L, im.W, im.H)
	f.Theta = axisAngle(L, im.W, im.H, f.Cx, f.Cy)
	f.Quality = sharpness(L, im.W, im.H)
	return f, nil
}

// sharpness: 行星区域内 DoG 能量 / 亮度²，与亮度无关的清晰度指标。
func sharpness(L []float64, w, h int) float64 {
	g1 := gaussBlur(L, w, h, 1.0)
	g2 := gaussBlur(L, w, h, 2.5)
	mx := percentile(L, 99.9)
	var e, s, n float64
	for i := range L {
		if L[i] < 0.2*mx {
			continue
		}
		d := g1[i] - g2[i]
		e += d * d
		s += L[i]
		n++
	}
	if n == 0 {
		return 0
	}
	m := s / n
	return e / n / (m * m)
}

// srcPos 计算画布坐标 (x,y) 在帧 f 原图中的位置（旋转+平移对齐到参考帧方向/中心）。
func srcPos(f *Frame, x, y, thetaRef float64, w, h int) (float64, float64) {
	d := f.Theta - thetaRef
	for d > math.Pi/2 {
		d -= math.Pi
	}
	for d < -math.Pi/2 {
		d += math.Pi
	}
	cx, cy := float64(w-1)/2, float64(h-1)/2
	dx, dy := x-f.Sx-cx, y-f.Sy-cy
	c, s := math.Cos(d), math.Sin(d)
	return f.Cx + dx*c - dy*s, f.Cy + dx*s + dy*c
}

// warpFrame 把帧重采样到参考画布；derot 非 nil 时先做消自转映射（只作用在本体软掩膜内）。
// 返回图像与逐像素权重（转到背面附近的点降权）。
func warpFrame(f *Frame, thetaRef float64, g *Geom, phi float64, bodyMask []float64) (*Img, []float64) {
	w, h := f.Im.W, f.Im.H
	out := NewImg(w, h)
	wt := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			px, py := float64(x), float64(y)
			wt[i] = 1
			if g != nil && bodyMask[i] > 0.001 {
				qx, qy, ok := g.derot(px, py, phi)
				m := bodyMask[i]
				px, py = px+m*(qx-px), py+m*(qy-py)
				if !ok {
					wt[i] = 1 - 0.95*m
				}
			}
			sx, sy := srcPos(f, px, py, thetaRef, w, h)
			for c := 0; c < 3; c++ {
				out.P[c][i] = sampleCubic(f.Im.P[c], w, h, sx, sy)
			}
		}
	}
	return out, wt
}

func stackFrames(frames []*Frame, thetaRef float64, g *Geom, tRef, omega, dir float64, bodyMask []float64) *Img {
	w, h := frames[0].Im.W, frames[0].Im.H
	acc := NewImg(w, h)
	den := make([]float64, w*h)
	for _, f := range frames {
		im, wt := warpFrame(f, thetaRef, g, dir*omega*(f.T-tRef), bodyMask)
		for i := range den {
			k := f.Weight * wt[i]
			for c := 0; c < 3; c++ {
				acc.P[c][i] += k * im.P[c][i]
			}
			den[i] += k
		}
	}
	for i := range den {
		for c := 0; c < 3; c++ {
			acc.P[c][i] /= den[i]
		}
	}
	return acc
}

// derotScore: 消自转后各帧本体高频细节与参考帧的加权相关系数，用来自动判定自转方向。
func derotScore(frames []*Frame, ref int, thetaRef float64, g *Geom, tRef, omega, dir float64, bodyMask []float64) float64 {
	w, h := frames[0].Im.W, frames[0].Im.H
	hp := func(f *Frame) []float64 {
		im, _ := warpFrame(f, thetaRef, g, dir*omega*(f.T-tRef), bodyMask)
		L := im.Lum()
		b := gaussBlur(L, w, h, 3)
		for i := range L {
			L[i] -= b[i]
		}
		return gaussBlur(L, w, h, 1)
	}
	R := hp(frames[ref])
	score, ws := 0.0, 0.0
	for k, f := range frames {
		if k == ref {
			continue
		}
		A := hp(f)
		var sab, saa, sbb float64
		for i := range A {
			m := bodyMask[i]
			if m < 0.99 || g.globeRho(float64(i%w), float64(i/w)) > 0.8 {
				continue
			}
			sab += A[i] * R[i]
			saa += A[i] * A[i]
			sbb += R[i] * R[i]
		}
		score += f.Weight * sab / math.Sqrt(saa*sbb+1e-30)
		ws += f.Weight
	}
	return score / ws
}
