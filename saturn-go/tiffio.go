package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// readTIFF 读取未压缩的 8/16 位 RGB 或灰度 TIFF（AutoStakkert / PIPP 输出格式），归一化到 [0,1]。
func readTIFF(path string) (*Img, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 8 {
		return nil, fmt.Errorf("%s: 文件太短", path)
	}
	var bo binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("%s: 不是 TIFF", path)
	}
	if bo.Uint16(b[2:]) != 42 {
		return nil, fmt.Errorf("%s: 不支持 BigTIFF", path)
	}
	ifd := int(bo.Uint32(b[4:]))
	n := int(bo.Uint16(b[ifd:]))
	typeSize := map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8}
	tags := map[uint16][]uint32{}
	for i := 0; i < n; i++ {
		e := b[ifd+2+12*i:]
		tag, typ, cnt := bo.Uint16(e), bo.Uint16(e[2:]), int(bo.Uint32(e[4:]))
		sz := typeSize[typ] * cnt
		data := e[8:12]
		if sz > 4 {
			off := int(bo.Uint32(e[8:]))
			data = b[off : off+sz]
		}
		var vals []uint32
		for j := 0; j < cnt; j++ {
			switch typ {
			case 1:
				vals = append(vals, uint32(data[j]))
			case 3:
				vals = append(vals, uint32(bo.Uint16(data[2*j:])))
			case 4:
				vals = append(vals, bo.Uint32(data[4*j:]))
			}
		}
		tags[tag] = vals
	}
	get := func(t uint16, def uint32) uint32 {
		if v, ok := tags[t]; ok && len(v) > 0 {
			return v[0]
		}
		return def
	}
	w, h := int(get(256, 0)), int(get(257, 0))
	bps := int(get(258, 8))
	spp := int(get(277, 1))
	if c := get(259, 1); c != 1 {
		return nil, fmt.Errorf("%s: 不支持压缩 TIFF (compression=%d)", path, c)
	}
	if get(284, 1) != 1 {
		return nil, fmt.Errorf("%s: 只支持 chunky planar 配置", path)
	}
	if bps != 8 && bps != 16 {
		return nil, fmt.Errorf("%s: 不支持 %d 位", path, bps)
	}
	var raw []byte
	offs, cnts := tags[273], tags[279]
	for i := range offs {
		raw = append(raw, b[offs[i]:offs[i]+cnts[i]]...)
	}
	im := NewImg(w, h)
	bpp := bps / 8
	maxv := float64(int(1)<<bps - 1)
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			s := c
			if spp < 3 {
				s = 0
			}
			k := (i*spp + s) * bpp
			var v float64
			if bpp == 1 {
				v = float64(raw[k])
			} else {
				v = float64(bo.Uint16(raw[k:]))
			}
			im.P[c][i] = v / maxv
		}
	}
	return im, nil
}

// writeTIFF16 写 16 位 RGB 未压缩 TIFF（方便再进 PS / PixInsight 继续调）。
func writeTIFF16(path string, im *Img) error {
	le := binary.LittleEndian
	dataLen := im.W * im.H * 6
	buf := make([]byte, 8+dataLen+6)
	copy(buf, "II")
	le.PutUint16(buf[2:], 42)
	for i := 0; i < im.W*im.H; i++ {
		for c := 0; c < 3; c++ {
			le.PutUint16(buf[8+i*6+c*2:], uint16(clamp01(im.P[c][i])*65535+0.5))
		}
	}
	bpsOff := 8 + dataLen
	for c := 0; c < 3; c++ {
		le.PutUint16(buf[bpsOff+2*c:], 16)
	}
	ifdOff := len(buf)
	le.PutUint32(buf[4:], uint32(ifdOff))
	type ent struct {
		tag, typ uint16
		cnt, val uint32
	}
	ents := []ent{
		{256, 4, 1, uint32(im.W)},
		{257, 4, 1, uint32(im.H)},
		{258, 3, 3, uint32(bpsOff)},
		{259, 3, 1, 1},
		{262, 3, 1, 2},
		{273, 4, 1, 8},
		{277, 3, 1, 3},
		{278, 4, 1, uint32(im.H)},
		{279, 4, 1, uint32(dataLen)},
		{284, 3, 1, 1},
	}
	ifd := make([]byte, 2+12*len(ents)+4)
	le.PutUint16(ifd, uint16(len(ents)))
	for i, e := range ents {
		p := ifd[2+12*i:]
		le.PutUint16(p, e.tag)
		le.PutUint16(p[2:], e.typ)
		le.PutUint32(p[4:], e.cnt)
		if e.typ == 3 && e.cnt == 1 {
			le.PutUint16(p[8:], uint16(e.val))
		} else {
			le.PutUint32(p[8:], e.val)
		}
	}
	return os.WriteFile(path, append(buf, ifd...), 0o644)
}
