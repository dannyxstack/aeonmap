package api

import (
	"archive/zip"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mapsim/export"
	"mapsim/platform"
	"mapsim/sim"
	"mapsim/store"
	"mapsim/transport"
	"mapsim/world"
)

var allFormats = []string{"geotiff", "geojson", "csv", "engine"}

// staticLayers：导出为 GeoTIFF 的世界图层。
var staticLayers = []string{"elev", "t_ann", "t_cold", "t_warm", "p_ann", "mi", "koppen", "landcover", "discharge",
	"freshwater", "npp", "density", "mode", "friction", "cap_dry", "cap_rice", "cap_irr", "cap_past", "cap_fish"}

type zipper struct {
	z     *zip.Writer
	files []string
}

func (zz *zipper) add(name string, write func(io.Writer) error) error {
	w, err := zz.z.Create(name)
	if err != nil {
		return err
	}
	zz.files = append(zz.files, name)
	return write(w)
}

func (zz *zipper) json(name string, v any) error {
	return zz.add(name, func(w io.Writer) error {
		e := json.NewEncoder(w)
		e.SetEscapeHTML(false)
		return e.Encode(v)
	})
}

// buildExport 生成导出包。runID 为空时只导出世界的静态图层。
func (r *Runner) buildExport(path string, pw *platform.World, w *world.World, runID string, req *ExportReq) error {
	formats := req.Formats
	if len(formats) == 0 {
		formats = allFormats
	}
	has := map[string]bool{}
	for _, f := range formats {
		has[strings.ToLower(f)] = true
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zz := &zipper{z: zip.NewWriter(f)}
	g := w.Manifest.Grid
	raster := func(desc string) *export.Raster {
		return &export.Raster{W: w.NX, H: w.NY, West: g.West, North: g.North, Res: g.Res, Description: desc}
	}
	readme := &strings.Builder{}
	fmt.Fprintf(readme, "# 世界推演导出包\n\n- 世界：%s (%s)\n- 引擎版本：%s\n- 预处理：%s\n- 网格：%d×%d，%.4g°，西 %.4g 北 %.4g\n- 导出时间：%s\n",
		pw.Name, pw.ID, sim.EngineVersion, filepath.Base(w.Dir), w.NX, w.NY, g.Res, g.West, g.North, time.Now().UTC().Format(time.RFC3339))

	// ---- 世界(静态)图层 ----
	if has["geotiff"] {
		for _, id := range staticLayers {
			arr, ok := w.F[id]
			if !ok {
				continue
			}
			fd := fieldDesc(w, id)
			rs := raster(fd.Name + " " + fd.Unit)
			if fd.Style == "categorical" {
				u := make([]uint8, len(arr))
				for i, v := range arr {
					u[i] = uint8(v)
				}
				rs.U8 = u
				if err := zz.json("world/"+id+"_legend.json", map[string]any{"labels": fd.Labels, "colors": fd.Colors}); err != nil {
					return err
				}
			} else {
				rs.F32 = arr
			}
			if err := zz.add("world/"+id+".tif", func(wr io.Writer) error { return export.WriteGeoTIFF(wr, rs) }); err != nil {
				return err
			}
		}
	}
	if has["geojson"] {
		if b, err := os.ReadFile(filepath.Join(w.Dir, "rivers.geojson")); err == nil {
			zz.add("world/rivers.geojson", func(wr io.Writer) error { _, err := wr.Write(b); return err })
		}
	}
	if has["engine"] {
		if err := r.engineAssets(zz, w, req.EngineSize); err != nil {
			return err
		}
	}
	fmt.Fprintf(readme, "\n## world/\n世界的静态图层(GeoTIFF，EPSG:4326)：海拔、气候、柯本、地表覆盖、河流流量、资源承载、潜在人口密度等；分类图层附 *_legend.json。rivers.geojson 为河道折线。\n")

	// ---- 演化结果 ----
	if runID != "" {
		rd, err := r.E.Reader(runID)
		if err != nil {
			return err
		}
		idx := rd.Index()
		if len(idx) == 0 {
			return fmt.Errorf("演化还没有帧")
		}
		year := int(idx[len(idx)-1].Year)
		if req.Year != nil && *req.Year >= 0 {
			year = *req.Year
		}
		raw, fy, err := rd.Frame(year)
		if err != nil {
			return err
		}
		fr, err := sim.DecodeFrame(raw, w.LandIdx, w.N)
		if err != nil {
			return err
		}
		names := polityNames(filepath.Join(r.E.RunsDir, runID))
		pfx := fmt.Sprintf("run/year_%d/", fy)
		m := rd.Meta
		pj, _ := json.MarshalIndent(m.Params, "", "  ")
		fmt.Fprintf(readme, "\n## run/\n- 演化：%s，种子 %d，共 %d 年，导出年份 %d\n- 复现键：%s(同一引擎版本、参数、约束与世界可得到完全相同的历史)\n- 参数：\n```json\n%s\n```\n",
			runID, m.Params.Seed, m.Params.Years, fy, m.Key, pj)
		if m.Constraints != nil {
			cj, _ := json.MarshalIndent(m.Constraints, "", "  ")
			fmt.Fprintf(readme, "- 约束：\n```json\n%s\n```\n", cj)
		}
		// 政权表(本帧)
		type pol struct {
			Index    int     `json:"index"`
			UID      int64   `json:"uid"`
			Name     string  `json:"name"`
			Pop      float64 `json:"pop"`
			Families uint32  `json:"families"`
			Cells    uint32  `json:"cells"`
			CapLon   float64 `json:"capitalLon"`
			CapLat   float64 `json:"capitalLat"`
		}
		var pols []pol
		capitals := map[int64]int32{}
		for k, p := range fr.Pols {
			q := pol{Index: k + 1, UID: p.UID, Name: names.name(p.UID), Pop: float64(p.Pop), Families: p.Families, Cells: p.Cells}
			if p.Capital >= 0 && int(p.Capital) < w.N {
				q.CapLon, q.CapLat = float64(w.Lon[p.Capital]), float64(w.Lat[p.Capital])
				if p.Cells >= 6 {
					capitals[p.UID] = p.Capital
				}
			}
			pols = append(pols, q)
		}
		polity := make([]int64, w.N)
		ids := make([]int32, w.N)
		for c := 0; c < w.N; c++ {
			polity[c] = fr.PolUID(fr.Top1[c])
			if fr.Top1[c] != 0 && fr.Top1[c] != sim.PolScattered {
				ids[c] = int32(fr.Top1[c])
			}
		}
		if has["geotiff"] {
			dens := make([]float32, w.N)
			for _, c := range w.LandIdx {
				if w.Area[c] > 0 {
					dens[c] = fr.Pop[c] / w.Area[c]
				}
			}
			zz.add(pfx+"population_density.tif", func(wr io.Writer) error {
				rs := raster("人口密度 人/km²")
				rs.F32 = dens
				return export.WriteGeoTIFF(wr, rs)
			})
			zz.add(pfx+"polity.tif", func(wr io.Writer) error {
				rs := raster("政权编号(见 polities.csv 的 index；0 无，65535 零散小族)")
				rs.U16 = fr.Top1
				return export.WriteGeoTIFF(wr, rs)
			})
			zz.add(pfx+"tribe.tif", func(wr io.Writer) error {
				rs := raster("部落(第4层)编号，同 polities.csv")
				rs.U16 = fr.Tribe
				return export.WriteGeoTIFF(wr, rs)
			})
			zz.add(pfx+"livelihood.tif", func(wr io.Writer) error {
				rs := raster("演化后的主导生计方式(编码见 world/mode_legend.json)")
				rs.U8 = fr.DMode
				return export.WriteGeoTIFF(wr, rs)
			})
		}
		if has["csv"] {
			zz.add(pfx+"polities.csv", func(wr io.Writer) error {
				cw := csv.NewWriter(wr)
				cw.Write([]string{"index", "uid", "name", "population", "families", "cells", "capital_lon", "capital_lat"})
				for _, p := range pols {
					cw.Write([]string{strconv.Itoa(p.Index), strconv.FormatInt(p.UID, 10), p.Name, ff(p.Pop, 0),
						strconv.Itoa(int(p.Families)), strconv.Itoa(int(p.Cells)), ff(p.CapLon, 4), ff(p.CapLat, 4)})
				}
				cw.Flush()
				return cw.Error()
			})
			if err := r.timelineCSV(zz, filepath.Join(r.E.RunsDir, runID), names); err != nil {
				return err
			}
		}
		if has["geojson"] {
			polys := export.Polygons(ids, w.NX, w.NY, g.West, g.North, g.Res)
			fc := export.NewFC()
			keys := make([]int32, 0, len(polys))
			for k := range polys {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
			for _, k := range keys {
				p := pols[k-1]
				fc.Features = append(fc.Features, export.Feature{Type: "Feature",
					Properties: map[string]any{"index": p.Index, "uid": p.UID, "name": p.Name, "population": math.Round(p.Pop),
						"families": p.Families, "cells": p.Cells},
					Geometry: export.Geometry{Type: "MultiPolygon", Coordinates: polys[k]}})
			}
			zz.json(pfx+"borders.geojson", fc)
			net := transport.Build(w, fr.Pop, polity, capitals, transport.DefaultOptions())
			roads, cities := networkGeoJSON(w, net, names)
			zz.json(pfx+"roads.geojson", roads)
			zz.json(pfx+"cities.geojson", cities)
		}
		fmt.Fprintf(readme, "- %s：人口密度、政权、部落、生计方式(GeoTIFF)；polities.csv；borders.geojson(政权疆域多边形)；roads.geojson(道路/航线，tier 1 大道 2 道路 3 小路)；cities.geojson\n- run/metrics.csv、run/events.csv：逐年指标与政权事件\n", pfx)
	}
	if has["engine"] {
		fmt.Fprintf(readme, "\n## engine/\n- heightmap.png：16 位灰度(Unreal 可直接导入)，heightmap.raw：16 位小端(Unity 地形导入)；高度 = min + v/65535×(max-min)，见 heightmap.json\n- splatmap.png：RGBA 地表混合权重(R 草原/灌丛，G 森林，B 荒漠/裸地，A 雪/岩/苔原)\n- biome.png + biome_legend.json：地表覆盖编号图\n")
	}
	zz.add("README.md", func(wr io.Writer) error { _, err := io.WriteString(wr, readme.String()); return err })
	return zz.z.Close()
}

func ff(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }

func fieldDesc(w *world.World, id string) world.FieldDesc {
	for _, f := range w.Manifest.Fields {
		if f.ID == id {
			return f
		}
	}
	return world.FieldDesc{ID: id, Name: id}
}

// engineAssets：游戏引擎用的正方形高程图与地表混合贴图(优先使用预处理的高分辨率高程)。
func (r *Runner) engineAssets(zz *zipper, w *world.World, size int) error {
	if size <= 0 {
		size = 1025
	}
	size = min(max(size, 129), 4097)
	elev, lc, W, H := baseRasters(w)
	h := export.Resample(elev, W, H, size)
	var lo, hi float32
	if err := zz.add("engine/heightmap.png", func(wr io.Writer) error {
		var err error
		lo, hi, err = export.HeightPNG16(wr, h, size)
		return err
	}); err != nil {
		return err
	}
	zz.add("engine/heightmap.raw", func(wr io.Writer) error { return export.HeightRAW16(wr, h, lo, hi) })
	g := w.Manifest.Grid
	zz.json("engine/heightmap.json", map[string]any{"size": size, "minMeters": lo, "maxMeters": hi,
		"bounds": map[string]float64{"west": g.West, "north": g.North, "east": g.West + float64(w.NX)*g.Res, "south": g.North - float64(w.NY)*g.Res},
		"note":   "高度 = minMeters + v/65535×(maxMeters-minMeters)；RAW 为 16 位小端，Unity 导入时选择 Windows 字节序"})
	lcs := export.ResampleNearest(lc, W, H, size)
	zz.add("engine/splatmap.png", func(wr io.Writer) error { return export.SplatPNG(wr, lcs, size) })
	zz.add("engine/biome.png", func(wr io.Writer) error { return export.IndexPNG(wr, lcs, size, size) })
	fd := fieldDesc(w, "landcover")
	return zz.json("engine/biome_legend.json", map[string]any{"labels": fd.Labels, "colors": fd.Colors})
}

// baseRasters 读取预处理的计算网格高程与地表覆盖；旧版缓存没有时退回模拟格数据。
func baseRasters(w *world.World) ([]float32, []uint8, int, int) {
	var mf struct {
		Base *struct {
			NX, NY int
			Files  map[string]string
		} `json:"base"`
	}
	if b, err := os.ReadFile(filepath.Join(w.Dir, "manifest.json")); err == nil {
		json.Unmarshal(b, &mf)
	}
	if mf.Base != nil {
		eb, err1 := os.ReadFile(filepath.Join(w.Dir, mf.Base.Files["elev"]))
		lb, err2 := os.ReadFile(filepath.Join(w.Dir, mf.Base.Files["landcover"]))
		n := mf.Base.NX * mf.Base.NY
		if err1 == nil && err2 == nil && len(eb) == 4*n && len(lb) == n {
			e := make([]float32, n)
			for i := range e {
				e[i] = math.Float32frombits(binary.LittleEndian.Uint32(eb[4*i:]))
			}
			return e, lb, mf.Base.NX, mf.Base.NY
		}
	}
	e := make([]float32, w.N)
	l := make([]uint8, w.N)
	for i := 0; i < w.N; i++ {
		if w.Land[i] {
			e[i] = w.F["elev"][i]
		} else {
			e[i] = -200
		}
		l[i] = uint8(w.F["landcover"][i])
	}
	return e, l, w.NX, w.NY
}

func networkGeoJSON(w *world.World, net *transport.Network, names nameBook) (*export.FeatureCollection, *export.FeatureCollection) {
	roads, cities := export.NewFC(), export.NewFC()
	for _, rt := range net.Routes {
		coords := make([][2]float64, 0, len(rt.Cells))
		for k := len(rt.Cells) - 1; k >= 0; k-- {
			c := rt.Cells[k]
			coords = append(coords, [2]float64{float64(w.Lon[c]), float64(w.Lat[c])})
		}
		roads.Features = append(roads.Features, export.Feature{Type: "Feature",
			Properties: map[string]any{"from": rt.From, "to": rt.To, "tier": rt.Tier, "sea": rt.Sea,
				"traffic": math.Round(rt.Traffic*100) / 100, "costKm": math.Round(rt.CostKm), "lengthKm": math.Round(rt.LengthKm)},
			Geometry: export.Geometry{Type: "LineString", Coordinates: coords}})
	}
	for i, c := range net.Cities {
		props := map[string]any{"id": i, "population": math.Round(c.Pop), "tier": c.Tier, "capital": c.Capital}
		if c.Polity != 0 {
			props["polity"] = c.Polity
			props["polityName"] = names.name(c.Polity)
		}
		cities.Features = append(cities.Features, export.Feature{Type: "Feature", Properties: props,
			Geometry: export.Geometry{Type: "Point", Coordinates: [2]float64{c.Lon, c.Lat}}})
	}
	return roads, cities
}

// nameBook：政权名(用户约束的名字优先，否则按 UID 生成)。
type nameBook map[int64]string

func (n nameBook) name(uid int64) string {
	if s, ok := n[uid]; ok && s != "" {
		return s
	}
	return sim.PolityName(uid)
}

func polityNames(runDir string) nameBook {
	nb := nameBook{}
	var list []sim.PolityInfo
	if b, err := os.ReadFile(filepath.Join(runDir, "polities.json")); err == nil {
		json.Unmarshal(b, &list)
	}
	for _, p := range list {
		nb[p.UID] = p.Name
	}
	return nb
}

func (r *Runner) timelineCSV(zz *zipper, runDir string, names nameBook) error {
	ms, _ := store.ReadMetrics(runDir)
	if err := zz.add("run/metrics.csv", func(wr io.Writer) error {
		cw := csv.NewWriter(wr)
		cw.Write([]string{"year", "families", "population", "polities_ge10", "largest_share", "top5_share", "supers", "wars", "annexations", "rebellions"})
		for _, m := range ms {
			cw.Write([]string{strconv.Itoa(m.Year), strconv.Itoa(m.Families), ff(m.Population, 0), strconv.Itoa(m.Polities10),
				ff(m.Largest, 4), ff(m.Top5, 4), strconv.Itoa(m.Supers), strconv.Itoa(m.Wars), strconv.Itoa(m.Annexations), strconv.Itoa(m.Rebellions)})
		}
		cw.Flush()
		return cw.Error()
	}); err != nil {
		return err
	}
	ev, _ := store.ReadEvents(runDir, 0, math.MaxInt32)
	return zz.add("run/events.csv", func(wr io.Writer) error {
		cw := csv.NewWriter(wr)
		cw.Write([]string{"year", "type", "a_uid", "a_name", "b_uid", "b_name", "n"})
		for _, e := range ev {
			bn := ""
			if e.B != 0 {
				bn = names.name(e.B)
			}
			cw.Write([]string{strconv.Itoa(e.Year), e.Type, strconv.FormatInt(e.A, 10), names.name(e.A),
				strconv.FormatInt(e.B, 10), bn, strconv.Itoa(e.N)})
		}
		cw.Flush()
		return cw.Error()
	})
}
