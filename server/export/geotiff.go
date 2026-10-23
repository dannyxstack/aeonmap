// Package export 把世界与演化结果导出为标准格式：GeoTIFF、16 位 PNG、GeoJSON、CSV、游戏引擎资源。
package export

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"strconv"
)

// Raster 是规则经纬网格上的单波段栅格(行优先，北→南)。
type Raster struct {
	W, H        int
	West, North float64
	Res         float64
	F32         []float32 // 二选一
	U8          []uint8
	U16         []uint16
	NoData      *float64
	Description string
}

type tiffTag struct {
	id    uint16
	typ   uint16 // 2 ASCII, 3 SHORT, 4 LONG, 12 DOUBLE
	count uint32
	data  []byte
}

// WriteGeoTIFF 写出未压缩、单条带的 GeoTIFF(EPSG:4326，像元为面)。GDAL、QGIS、tifffile 均可读取。
func WriteGeoTIFF(w io.Writer, r *Raster) error {
	le := binary.LittleEndian
	var pix []byte
	var bits, sfmt uint16
	switch {
	case r.F32 != nil:
		pix = make([]byte, 4*len(r.F32))
		for i, v := range r.F32 {
			le.PutUint32(pix[4*i:], math.Float32bits(v))
		}
		bits, sfmt = 32, 3
	case r.U16 != nil:
		pix = make([]byte, 2*len(r.U16))
		for i, v := range r.U16 {
			le.PutUint16(pix[2*i:], v)
		}
		bits, sfmt = 16, 1
	default:
		pix = append([]byte(nil), r.U8...)
		bits, sfmt = 8, 1
	}
	short := func(v ...uint16) []byte {
		b := make([]byte, 2*len(v))
		for i, x := range v {
			le.PutUint16(b[2*i:], x)
		}
		return b
	}
	long := func(v ...uint32) []byte {
		b := make([]byte, 4*len(v))
		for i, x := range v {
			le.PutUint32(b[4*i:], x)
		}
		return b
	}
	dbl := func(v ...float64) []byte {
		b := make([]byte, 8*len(v))
		for i, x := range v {
			le.PutUint64(b[8*i:], math.Float64bits(x))
		}
		return b
	}
	geokeys := []uint16{
		1, 1, 0, 3, // 版本 1.1.0，3 个键
		1024, 0, 1, 2, // GTModelType = Geographic
		1025, 0, 1, 1, // GTRasterType = PixelIsArea
		2048, 0, 1, 4326, // GeographicType = WGS84
	}
	tags := []tiffTag{
		{256, 4, 1, long(uint32(r.W))},
		{257, 4, 1, long(uint32(r.H))},
		{258, 3, 1, short(bits)},
		{259, 3, 1, short(1)}, // 不压缩
		{262, 3, 1, short(1)}, // BlackIsZero
		{273, 4, 1, nil},      // StripOffsets，稍后填
		{277, 3, 1, short(1)},
		{278, 4, 1, long(uint32(r.H))},
		{279, 4, 1, long(uint32(len(pix)))},
		{284, 3, 1, short(1)},
		{339, 3, 1, short(sfmt)},
		{33550, 12, 3, dbl(r.Res, r.Res, 0)},
		{33922, 12, 6, dbl(0, 0, 0, r.West, r.North, 0)},
		{34735, 3, uint32(len(geokeys)), short(geokeys...)},
	}
	if r.Description != "" {
		s := append([]byte(r.Description), 0)
		tags = append(tags, tiffTag{270, 2, uint32(len(s)), s})
	}
	if r.NoData != nil {
		s := append([]byte(formatFloat(*r.NoData)), 0)
		tags = append(tags, tiffTag{42113, 2, uint32(len(s)), s})
	}
	// 按标签号排序(TIFF 要求)
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j].id < tags[j-1].id; j-- {
			tags[j], tags[j-1] = tags[j-1], tags[j]
		}
	}
	// 布局：头(8) + IFD + 溢出数据(各自按偶数字节对齐) + 像素
	ifdSize := 2 + 12*len(tags) + 4
	extraOff := 8 + ifdSize
	offs := make([]int, len(tags))
	cur := extraOff
	for i := range tags {
		if len(tags[i].data) > 4 {
			cur += cur % 2
			offs[i] = cur
			cur += len(tags[i].data)
		}
	}
	pixOff := cur + cur%2
	for i := range tags {
		if tags[i].id == 273 {
			tags[i].data = long(uint32(pixOff))
		}
	}
	var out bytes.Buffer
	out.Write([]byte{'I', 'I', 42, 0})
	out.Write(long(8))
	out.Write(short(uint16(len(tags))))
	for i, t := range tags {
		out.Write(short(t.id, t.typ))
		out.Write(long(t.count))
		if len(t.data) <= 4 {
			v := make([]byte, 4)
			copy(v, t.data)
			out.Write(v)
		} else {
			out.Write(long(uint32(offs[i])))
		}
	}
	out.Write(long(0)) // 无下一个 IFD
	for i, t := range tags {
		if len(t.data) > 4 {
			for out.Len() < offs[i] {
				out.WriteByte(0)
			}
			out.Write(t.data)
		}
	}
	for out.Len() < pixOff {
		out.WriteByte(0)
	}
	out.Write(pix)
	_, err := w.Write(out.Bytes())
	return err
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
