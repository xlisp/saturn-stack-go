package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/cmplx"
	"os"
	"sort"
)

// Img 是线性浮点 RGB 图像。
type Img struct {
	W, H int
	P    [3][]float64
}

func NewImg(w, h int) *Img {
	im := &Img{W: w, H: h}
	for c := range im.P {
		im.P[c] = make([]float64, w*h)
	}
	return im
}

func (m *Img) Clone() *Img {
	o := NewImg(m.W, m.H)
	for c := range m.P {
		copy(o.P[c], m.P[c])
	}
	return o
}

func (m *Img) Lum() []float64 {
	l := make([]float64, m.W*m.H)
	for i := range l {
		l[i] = 0.299*m.P[0][i] + 0.587*m.P[1][i] + 0.114*m.P[2][i]
	}
	return l
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func smoothstep(e0, e1, x float64) float64 {
	t := clamp01((x - e0) / (e1 - e0))
	return t * t * (3 - 2*t)
}

// ---------- 插值 ----------

func cubicW(t float64) [4]float64 { // Catmull-Rom
	t2, t3 := t*t, t*t*t
	return [4]float64{
		-0.5*t3 + t2 - 0.5*t,
		1.5*t3 - 2.5*t2 + 1,
		-1.5*t3 + 2*t2 + 0.5*t,
		0.5*t3 - 0.5*t2,
	}
}

func sampleCubic(ch []float64, w, h int, x, y float64) float64 {
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	if ix < -2 || iy < -2 || ix > w || iy > h {
		return 0
	}
	wx, wy := cubicW(x-float64(ix)), cubicW(y-float64(iy))
	s := 0.0
	for j := 0; j < 4; j++ {
		yy := iy - 1 + j
		if yy < 0 {
			yy = 0
		} else if yy >= h {
			yy = h - 1
		}
		row := ch[yy*w:]
		r := 0.0
		for i := 0; i < 4; i++ {
			xx := ix - 1 + i
			if xx < 0 {
				xx = 0
			} else if xx >= w {
				xx = w - 1
			}
			r += wx[i] * row[xx]
		}
		s += wy[j] * r
	}
	return s
}

// ---------- 滤波 ----------

func gaussBlur(src []float64, w, h int, sigma float64) []float64 {
	if sigma <= 0 {
		return append([]float64(nil), src...)
	}
	r := int(math.Ceil(sigma * 3))
	k := make([]float64, 2*r+1)
	sum := 0.0
	for i := -r; i <= r; i++ {
		k[i+r] = math.Exp(-float64(i*i) / (2 * sigma * sigma))
		sum += k[i+r]
	}
	for i := range k {
		k[i] /= sum
	}
	return sepConv(src, w, h, k, 1)
}

// sepConv 可分离卷积，step 为核的采样间隔（à trous 用）。边界镜像。
func sepConv(src []float64, w, h int, k []float64, step int) []float64 {
	r := len(k) / 2
	tmp := make([]float64, w*h)
	out := make([]float64, w*h)
	mirror := func(i, n int) int {
		for i < 0 || i >= n {
			if i < 0 {
				i = -i
			}
			if i >= n {
				i = 2*n - 2 - i
			}
		}
		return i
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := 0.0
			for i := -r; i <= r; i++ {
				s += k[i+r] * src[y*w+mirror(x+i*step, w)]
			}
			tmp[y*w+x] = s
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := 0.0
			for i := -r; i <= r; i++ {
				s += k[i+r] * tmp[mirror(y+i*step, h)*w+x]
			}
			out[y*w+x] = s
		}
	}
	return out
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(p / 100 * float64(len(s)-1))
	return s[i]
}

// ---------- FFT / 相位相关 ----------

func fft(a []complex128, inv bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		ang := 2 * math.Pi / float64(l)
		if inv {
			ang = -ang
		}
		wl := complex(math.Cos(ang), -math.Sin(ang))
		for i := 0; i < n; i += l {
			w := complex(1, 0)
			for j := 0; j < l/2; j++ {
				u, v := a[i+j], a[i+j+l/2]*w
				a[i+j], a[i+j+l/2] = u+v, u-v
				w *= wl
			}
		}
	}
	if inv {
		for i := range a {
			a[i] /= complex(float64(n), 0)
		}
	}
}

func fft2(a []complex128, n int, inv bool) { // n×n
	row := make([]complex128, n)
	for y := 0; y < n; y++ {
		fft(a[y*n:(y+1)*n], inv)
	}
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			row[y] = a[y*n+x]
		}
		fft(row, inv)
		for y := 0; y < n; y++ {
			a[y*n+x] = row[y]
		}
	}
}

// phaseCorr 返回位移 (dx,dy)，使 a(x) ≈ b(x - d)。亚像素抛物线拟合。
func phaseCorr(a, b []float64, w, h int) (float64, float64) {
	n := 1
	for n < w || n < h {
		n <<= 1
	}
	A := make([]complex128, n*n)
	B := make([]complex128, n*n)
	ma, mb := mean(a), mean(b)
	for y := 0; y < h; y++ {
		wy := 0.5 - 0.5*math.Cos(2*math.Pi*float64(y)/float64(h-1))
		for x := 0; x < w; x++ {
			wx := 0.5 - 0.5*math.Cos(2*math.Pi*float64(x)/float64(w-1))
			A[y*n+x] = complex((a[y*w+x]-ma)*wx*wy, 0)
			B[y*n+x] = complex((b[y*w+x]-mb)*wx*wy, 0)
		}
	}
	fft2(A, n, false)
	fft2(B, n, false)
	sig := float64(n) / 6 // 低通，压制噪声主导的高频
	for v := 0; v < n; v++ {
		fv := float64(v)
		if v > n/2 {
			fv -= float64(n)
		}
		for u := 0; u < n; u++ {
			fu := float64(u)
			if u > n/2 {
				fu -= float64(n)
			}
			c := A[v*n+u] * cmplx.Conj(B[v*n+u])
			m := cmplx.Abs(c)
			g := math.Exp(-(fu*fu + fv*fv) / (2 * sig * sig))
			if m > 1e-20 {
				A[v*n+u] = c / complex(m, 0) * complex(g, 0)
			} else {
				A[v*n+u] = 0
			}
		}
	}
	fft2(A, n, true)
	best, bi := -1e300, 0
	for i, v := range A {
		if real(v) > best {
			best, bi = real(v), i
		}
	}
	px, py := bi%n, bi/n
	at := func(x, y int) float64 { return real(A[((y+n)%n)*n+(x+n)%n]) }
	sub := func(m, c, p float64) float64 {
		d := m - 2*c + p
		if d == 0 {
			return 0
		}
		return 0.5 * (m - p) / d
	}
	dx := float64(px) + sub(at(px-1, py), best, at(px+1, py))
	dy := float64(py) + sub(at(px, py-1), best, at(px, py+1))
	if dx > float64(n)/2 {
		dx -= float64(n)
	}
	if dy > float64(n)/2 {
		dy -= float64(n)
	}
	return dx, dy
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// ---------- 输出 ----------

func toRGBA64(im *Img) *image.RGBA64 {
	o := image.NewRGBA64(image.Rect(0, 0, im.W, im.H))
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := y*im.W + x
			o.SetRGBA64(x, y, color.RGBA64{
				uint16(clamp01(im.P[0][i])*65535 + 0.5),
				uint16(clamp01(im.P[1][i])*65535 + 0.5),
				uint16(clamp01(im.P[2][i])*65535 + 0.5),
				65535})
		}
	}
	return o
}

func savePNG(path string, im *Img) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, toRGBA64(im))
}

func saveJPEG(path string, im *Img) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, toRGBA64(im), &jpeg.Options{Quality: 95})
}
