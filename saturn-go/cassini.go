package main

import (
	"fmt"
	"math"
)

// 卡西尼缝（环平面）117580–122170 km，中心约 1.989 Re，宽 0.0762 Re。
const (
	cassiniR      = 1.989
	cassiniHalfW  = 0.0381 / cassiniR // 相对中心线椭圆的半宽
	cassiniSearch = 0.12              // 在 ±12% 范围内寻找暗缝
)

// Cassini 是卡西尼缝中心线在画布上的椭圆（沿 g 的 u/v 轴的半轴，像素）。
type Cassini struct {
	A, B  float64
	N     int // 参与拟合的点数
	Model bool
}

// fitCassini 在环脊（ansae）上沿环平面径向找亮度极小值，确定中心线椭圆 u²/A² + v²/B² = 1。
// 环脊处卡西尼缝宽 7 像素以上、数据里清楚可见，这里测得的位置用来约束中间段的绘制。
func fitCassini(lum []float64, w, h int, g Geom) Cassini {
	Ls := gaussBlur(cleanLum(lum, w, h), w, h, 0.8)
	sb := math.Abs(math.Sin(g.B))
	at := func(rho, c, s float64) float64 {
		x, y := g.xy(rho*g.Re*c, rho*g.Re*sb*s)
		return sampleCubic(Ls, w, h, x, y)
	}
	var rhos []float64
	for deg := 0.0; deg < 360; deg += 0.5 {
		c, s := math.Cos(deg*math.Pi/180), math.Sin(deg*math.Pi/180)
		if math.Abs(cassiniR*c) < 1.3 { // 只用环脊，避开本体和环挡本体的区段
			continue
		}
		const step = 0.004
		r0, r1 := cassiniR*(1-cassiniSearch), cassiniR*(1+cassiniSearch)
		var prof []float64
		for r := r0; r <= r1; r += step {
			prof = append(prof, at(r, c, s))
		}
		k := 0
		for i := range prof {
			if prof[i] < prof[k] {
				k = i
			}
		}
		if k == 0 || k == len(prof)-1 {
			continue
		}
		inMax, outMax := 0.0, 0.0
		for i := 0; i < k; i++ {
			inMax = math.Max(inMax, prof[i])
		}
		for i := k + 1; i < len(prof); i++ {
			outMax = math.Max(outMax, prof[i])
		}
		if prof[k] > 0.9*math.Min(inMax, outMax) { // 没有明显暗缝
			continue
		}
		d := prof[k-1] - 2*prof[k] + prof[k+1]
		off := 0.0
		if d > 0 {
			off = 0.5 * (prof[k-1] - prof[k+1]) / d
		}
		rhos = append(rhos, r0+(float64(k)+off)*step)
	}
	model := Cassini{A: cassiniR * g.Re, B: cassiniR * g.Re * sb, Model: true}
	if len(rhos) < 20 {
		fmt.Printf("  卡西尼缝拟合点不足 (%d)，使用几何模型\n", len(rhos))
		return model
	}
	// 环脊处暗缝主要约束长半轴；短半轴由 B（星历）给出——环脊在 v 方向几乎不可分辨，
	// 直接拟合短半轴会被环亮度沿方位角的梯度带偏。
	rho := percentile(rhos, 50)
	if math.Abs(rho/cassiniR-1) > 0.05 {
		fmt.Printf("  卡西尼缝位置 %.3f Re 偏离标称值过多，使用几何模型\n", rho)
		return model
	}
	cs := Cassini{A: rho * g.Re, B: rho * g.Re * sb, N: len(rhos)}
	fmt.Printf("  卡西尼缝拟合: %d 点  中心线 %.3f Re（标称 %.3f）  半轴 %.1f × %.1f px\n",
		cs.N, rho, cassiniR, cs.A, cs.B)
	return cs
}

// applyCassini 在输出分辨率上把卡西尼缝沿拟合椭圆压暗。
// 每个输出像素 4×4 超采样做抗锯齿；只作用于可见的环（近侧半边，或本体之外的远侧半边）。
// 环脊处与数据中的暗缝重合（加深），中间段投影宽度不到 1 原始像素，这里按环轴对称补出。
func applyCassini(out *Img, g Geom, cs Cassini, p Params, mapXY func(x, y float64) (float64, float64)) {
	if p.CassiniDepth <= 0 {
		return
	}
	N := out.W
	cov := make([]float64, N*N)
	const ss = 4
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			hit := 0
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					sx, sy := mapXY(float64(x)+(float64(i)+0.5)/ss-0.5, float64(y)+(float64(j)+0.5)/ss-0.5)
					u, v := g.uv(sx, sy)
					rho := math.Hypot(u/cs.A, v/cs.B)
					if math.Abs(rho-1) > cassiniHalfW*p.CassiniWidth {
						continue
					}
					front := v*g.B < 0
					if front || g.globeRho(sx, sy) > 1 {
						hit++
					}
				}
			}
			cov[y*N+x] = float64(hit) / ss / ss
		}
	}
	cov = gaussBlur(cov, N, N, 0.5)
	for i := range cov {
		f := 1 - p.CassiniDepth*math.Min(1, cov[i])
		for c := 0; c < 3; c++ {
			out.P[c][i] *= f
		}
	}
}
