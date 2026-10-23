package main

import "math"

// 低精度星历：JPL 行星近似轨道根数（1800–2050，J2000 黄道）+ IAU 土星自转轴。
// 计算地心看到的环张角 B，精度约 0.01°，足够约束环的投影几何（与 WinJUPOS 的做法一致）。

type kepler [6][2]float64 // a, e, I, L, ϖ, Ω：各为 {J2000 值, 每儒略世纪变化率}

var (
	elEarth  = kepler{{1.00000261, 0.00000562}, {0.01671123, -0.00004392}, {-0.00001531, -0.01294668}, {100.46457166, 35999.37244981}, {102.93768193, 0.32327364}, {0, 0}}
	elSaturn = kepler{{9.53667594, -0.00125060}, {0.05386179, -0.00050991}, {2.48599187, 0.00193609}, {49.95424423, 1222.49362201}, {92.59887831, -0.41897216}, {113.66242448, -0.28867794}}
)

func helio(k kepler, T float64) [3]float64 {
	f := func(p [2]float64) float64 { return p[0] + p[1]*T }
	a, e := f(k[0]), f(k[1])
	I, L, w, O := f(k[2])*math.Pi/180, f(k[3]), f(k[4]), f(k[5])
	M := math.Mod(L-w, 360) * math.Pi / 180
	E := M
	for n := 0; n < 30; n++ {
		E -= (E - e*math.Sin(E) - M) / (1 - e*math.Cos(E))
	}
	xp, yp := a*(math.Cos(E)-e), a*math.Sqrt(1-e*e)*math.Sin(E)
	om, Or := (w-O)*math.Pi/180, O*math.Pi/180
	co, so, cO, sO, cI, sI := math.Cos(om), math.Sin(om), math.Cos(Or), math.Sin(Or), math.Cos(I), math.Sin(I)
	return [3]float64{
		(co*cO-so*sO*cI)*xp + (-so*cO-co*sO*cI)*yp,
		(co*sO+so*cO*cI)*xp + (-so*sO+co*cO*cI)*yp,
		so*sI*xp + co*sI*yp,
	}
}

// julianDay 公历日期 + UT 小时 → 儒略日。
func julianDay(y, m, d int, hours float64) float64 {
	if m <= 2 {
		y--
		m += 12
	}
	A := y / 100
	B := 2 - A + A/4
	return math.Floor(365.25*float64(y+4716)) + math.Floor(30.6001*float64(m+1)) + float64(d) + float64(B) - 1524.5 + hours/24
}

// saturnB 返回该时刻地心环张角 B（度，南面朝向地球为负）。
func saturnB(jd float64) float64 {
	T := (jd - 2451545) / 36525
	s, e := helio(elSaturn, T), helio(elEarth, T)
	dx, dy, dz := s[0]-e[0], s[1]-e[1], s[2]-e[2]
	eps := 23.43928 * math.Pi / 180
	dy, dz = dy*math.Cos(eps)-dz*math.Sin(eps), dy*math.Sin(eps)+dz*math.Cos(eps)
	n := math.Sqrt(dx*dx + dy*dy + dz*dz)
	a0 := (40.589 - 0.036*T) * math.Pi / 180
	d0 := (83.537 - 0.004*T) * math.Pi / 180
	px, py, pz := math.Cos(d0)*math.Cos(a0), math.Cos(d0)*math.Sin(a0), math.Sin(d0)
	return math.Asin(-(dx*px+dy*py+dz*pz)/n) * 180 / math.Pi
}
