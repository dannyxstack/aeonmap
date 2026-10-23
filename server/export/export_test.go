package export

import (
	"bytes"
	"testing"
)

func TestPolygonsHoleAndOrientation(t *testing.T) {
	// 5x5，值 1 构成一个带 1 格洞的环，值 2 在洞里
	ids := make([]int32, 25)
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			if x >= 1 && x <= 3 && y >= 1 && y <= 3 {
				ids[y*5+x] = 1
			}
		}
	}
	ids[2*5+2] = 2
	p := Polygons(ids, 5, 5, 0, 5, 1)
	if len(p[1]) != 1 || len(p[1][0]) != 2 {
		t.Fatalf("值 1 应为 1 个带 1 个洞的多边形，得到 %v", p[1])
	}
	if ringArea(p[1][0][0]) <= 0 || ringArea(p[1][0][1]) >= 0 {
		t.Fatal("外环应逆时针、内洞应顺时针")
	}
	if a := ringArea(p[1][0][0]) + ringArea(p[1][0][1]); a != 8 {
		t.Fatalf("面积应为 8 格，得到 %v", a)
	}
	if len(p[2]) != 1 || ringArea(p[2][0][0]) != 1 {
		t.Fatal("值 2 应为 1 格")
	}
}

func TestGeoTIFFHeader(t *testing.T) {
	var b bytes.Buffer
	r := &Raster{W: 3, H: 2, West: 10, North: 50, Res: 0.5, F32: []float32{1, 2, 3, 4, 5, 6}}
	if err := WriteGeoTIFF(&b, r); err != nil {
		t.Fatal(err)
	}
	if string(b.Bytes()[:4]) != "II*\x00" {
		t.Fatal("TIFF 头错误")
	}
}
