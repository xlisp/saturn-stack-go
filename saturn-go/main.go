// saturn-go: 土星多视频叠加结果的消自转合成 + 本体/环分区后期。
//
//	go run . -in .. -out ../output
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func parseGains(s string) []float64 {
	var g []float64
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil {
			log.Fatalf("增益格式错误: %s", s)
		}
		g = append(g, v)
	}
	return g
}

func main() {
	in := flag.String("in", ".", "TIF 所在目录")
	out := flag.String("out", "output", "输出目录")
	period := flag.Float64("period", 10.5606, "土星自转周期（小时，System III）")
	wpow := flag.Float64("wpow", 3, "清晰度权重指数，越大越偏向最清晰的帧")
	dirFlag := flag.String("dir", "auto", "自转方向 auto|+1|-1")
	frontFlag := flag.String("front", "auto", "环近侧 auto|+1|-1")
	bDeg := flag.Float64("B", 0, "环张角 B（度，0=按文件名时间由星历计算）")
	useEphem := flag.Bool("ephem", true, "用星历计算 B（false 则从图像测量）")
	var p Params
	flag.Float64Var(&p.BodySat, "sat", 2.5, "本体饱和度倍数")
	flag.Float64Var(&p.RingSat, "ringsat", 0.55, "环饱和度倍数")
	bg := flag.String("bodygains", "1.0,1.6,1.9,1.5,1.2,1.05", "本体小波增益")
	rg := flag.String("ringgains", "1.0,2.2,2.3,1.6,1.15,1.0", "环小波增益")
	flag.Float64Var(&p.Denoise, "denoise", 0.8, "小波细层降噪强度")
	flag.Float64Var(&p.BodyGamma, "bodygamma", 0.92, "本体 gamma")
	flag.Float64Var(&p.RingGamma, "ringgamma", 1.25, "环 gamma")
	flag.Float64Var(&p.RingContrast, "ringcontrast", 1.15, "环对比度")
	flag.Float64Var(&p.WBMix, "wb", 0.85, "以环为中性灰的白平衡强度 0..1")
	flag.IntVar(&p.Size, "size", 1400, "输出边长（像素）")
	flag.Float64Var(&p.Rotate, "rot", 0, "输出旋转角度（度，逆时针）")
	flag.Float64Var(&p.Margin, "margin", 1.15, "裁切边距系数")
	flag.Float64Var(&p.CassiniDepth, "cassini", 0.6, "卡西尼缝压暗强度 0..1（0=关闭）")
	flag.Float64Var(&p.CassiniWidth, "cassiniwidth", 1.0, "卡西尼缝宽度倍数")
	flag.Parse()
	p.BodyGains, p.RingGains = parseGains(*bg), parseGains(*rg)

	paths, _ := filepath.Glob(filepath.Join(*in, "*.tif"))
	more, _ := filepath.Glob(filepath.Join(*in, "*.tiff"))
	paths = append(paths, more...)
	if len(paths) == 0 {
		log.Fatalf("%s 下没有 TIF", *in)
	}
	sort.Strings(paths)
	os.MkdirAll(*out, 0o755)

	// ---- 1. 载入 & 逐帧测量 ----
	var frames []*Frame
	for _, pth := range paths {
		f, err := loadFrame(pth)
		if err != nil {
			log.Fatal(err)
		}
		frames = append(frames, f)
	}
	ref := 0
	for k, f := range frames {
		if f.Quality > frames[ref].Quality {
			ref = k
		}
	}
	tRef, wsum := 0.0, 0.0
	for _, f := range frames {
		f.Weight = math.Pow(f.Quality/frames[ref].Quality, *wpow)
		tRef += f.Weight * f.T
		wsum += f.Weight
	}
	tRef /= wsum
	thetaRef := frames[ref].Theta
	fmt.Println("== 帧信息 ==")
	for k, f := range frames {
		mark := ""
		if k == ref {
			mark = "  ← 参考帧"
		}
		fmt.Printf("  %s  UT %s  中心(%.1f,%.1f)  环轴 %.2f°  清晰度 %.3g  权重 %.2f%s\n",
			f.Name, hm(f.T), f.Cx, f.Cy, f.Theta*180/math.Pi, f.Quality, f.Weight/wsum, mark)
	}
	fmt.Printf("  合成参考时刻 UT %s\n", hm(tRef))

	// ---- 2. 粗对齐 + 相位相关精对齐 ----
	w, h := frames[0].Im.W, frames[0].Im.H
	refIm, _ := warpFrame(frames[ref], thetaRef, nil, 0, nil)
	refL := cleanLum(refIm.Lum(), w, h)
	for k, f := range frames {
		if k == ref {
			continue
		}
		im, _ := warpFrame(f, thetaRef, nil, 0, nil)
		dx, dy := phaseCorr(refL, cleanLum(im.Lum(), w, h), w, h)
		f.Sx, f.Sy = dx, dy
	}
	S1 := stackFrames(frames, thetaRef, nil, tRef, 0, 0, nil)

	// ---- 3. 几何测量 ----
	g := estimateGeom(S1.Lum(), w, h)
	measuredB := g.B * 180 / math.Pi
	switch {
	case *bDeg != 0:
		g.B = math.Abs(*bDeg) * math.Pi / 180
	case *useEphem:
		jd0, err := parseJD(paths[0])
		must(err)
		eb := saturnB(jd0 + (tRef-frames[0].T)/24)
		fmt.Printf("  星历 B = %.2f°（图像测量 %.2f°）\n", eb, measuredB)
		g.B = math.Abs(eb) * math.Pi / 180
	}
	switch *frontFlag {
	case "+1":
	case "-1":
		g.B = -g.B
	default:
		gp, gm := g, g
		gm.B = -g.B
		L := cleanLum(S1.Lum(), w, h)
		sp, sm := frontScore(L, w, h, gp), frontScore(L, w, h, gm)
		fmt.Printf("  环近侧判定得分 +1: %.4f  -1: %.4f\n", sp, sm)
		if sm > sp {
			g.B = -g.B
		}
	}
	fmt.Printf("== 几何 ==\n  中心 (%.2f, %.2f)  环长轴 %.2f°  Re %.1f px  B %.2f°\n",
		g.Cx, g.Cy, g.Theta*180/math.Pi, g.Re, g.B*180/math.Pi)
	mk := g.masks(w, h, 2)

	// ---- 4. 消自转 ----
	omega := 2 * math.Pi / *period
	dir := 1.0
	switch *dirFlag {
	case "+1":
	case "-1":
		dir = -1
	default:
		sp := derotScore(frames, ref, thetaRef, &g, tRef, omega, 1, mk.Derot)
		sm := derotScore(frames, ref, thetaRef, &g, tRef, omega, -1, mk.Derot)
		s0 := derotScore(frames, ref, thetaRef, &g, tRef, 0, 1, mk.Derot)
		fmt.Printf("== 消自转 ==\n  相关性  不消: %.4f  方向+1: %.4f  方向-1: %.4f\n", s0, sp, sm)
		if sm > sp {
			dir = -1
		}
	}
	span := (frames[len(frames)-1].T - frames[0].T) * 360 / *period
	fmt.Printf("  采用方向 %+.0f，时间跨度内自转 %.1f°\n", dir, span)
	S := stackFrames(frames, thetaRef, &g, tRef, omega, dir, mk.Derot)
	must(writeTIFF16(filepath.Join(*out, "stack_derotated.tif"), normalizeForSave(S)))

	// ---- 5. 分区后期 ----
	fmt.Println("== 后期 ==")
	final, pm := postProcess(S, g, p)
	must(savePNG(filepath.Join(*out, "masks.png"), maskPreview(S, pm)))
	fin := framing(final, g, p)
	applyCassini(fin, g, fitCassini(S.Lum(), w, h, g), p, frameMap(g, p))
	must(savePNG(filepath.Join(*out, "saturn_final.png"), fin))
	must(saveJPEG(filepath.Join(*out, "saturn_final.jpg"), fin))
	must(writeTIFF16(filepath.Join(*out, "saturn_final.tif"), fin))
	fmt.Printf("完成 → %s\n", *out)
}

func normalizeForSave(S *Img) *Img {
	o := S.Clone()
	mx := percentile(S.Lum(), 99.95)
	for c := range o.P {
		for i := range o.P[c] {
			o.P[c][i] = clamp01(o.P[c][i] / mx * 0.9)
		}
	}
	return o
}

func hm(t float64) string {
	t = math.Mod(t, 24)
	hh := int(t)
	return fmt.Sprintf("%02d:%04.1f", hh, (t-float64(hh))*60)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
