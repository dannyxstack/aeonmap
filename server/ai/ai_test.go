package ai

import (
	"image"
	"image/color"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	for _, s := range []string{
		`{"a":1}`,
		"<think>先想想 {不是 json}</think>\n```json\n{\"a\":1}\n```",
		`结果如下：{"a":1,"s":"含 } 括号"} 以上`,
	} {
		if js, err := extractJSON(s); err != nil || js[0] != '{' {
			t.Errorf("extractJSON(%q) = %s, %v", s, js, err)
		}
	}
	if _, err := extractJSON(`{"a":1`); err == nil {
		t.Error("未闭合的 JSON 应报错")
	}
}

// 色块中夹着文字像素时，取色应取众数而不是平均。
func TestLegendSampling(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 100))
	cols := []color.RGBA{{20, 90, 60, 255}, {210, 210, 120, 255}, {150, 150, 150, 255}}
	for x := 0; x < 400; x++ {
		for y := 0; y < 100; y++ {
			c := cols[min(x*3/400, 2)]
			if y == 50 && x%3 == 0 { // 文字笔画
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	legend := buildLegend(img, []stop{{X: 833, Y: 500, ElevationM: 5000}, {X: 166, Y: 500, ElevationM: -30}, {X: 500, Y: 500, ElevationM: 500}})
	want := []string{"#145a3c", "#d2d278", "#969696"}
	if len(legend) != 3 {
		t.Fatalf("legend = %v", legend)
	}
	for i, w := range want {
		if legend[i][0] != w {
			t.Errorf("第 %d 档颜色 %v，应为 %s", i, legend[i][0], w)
		}
	}
	if legend[0][1] != -30.0 {
		t.Errorf("应按海拔排序：%v", legend)
	}
}

func TestPaletteNeedsSeaAndLand(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	pal, warn := buildPalette(img, []colorSample{{X: 500, Y: 500, Class: "mountain"}})
	if pal != nil || len(warn) == 0 {
		t.Error("缺少海和陆地样点时应退回内置规则")
	}
}
