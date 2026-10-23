package sim

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// 帧中每个格子的动态字段(顺序即编码顺序)。
var FrameFields = []struct {
	ID, Name, Unit, Group, Style, Desc string
	Bytes                              int
}{
	{"pop", "人口", "人", "人口", "continuous", "格内家族人口合计(对数量化)", 2},
	{"fam", "家族数", "个", "人口", "continuous", "格内家族数", 2},
	{"dmode", "生计方式(演化)", "", "人口", "categorical", "格内家族技能加权后的主导生计", 1},
	{"tribe", "部落", "", "社会", "categorical", "第4层：格内人口最多的最高首领", 2},
	{"top1", "政权", "", "政治", "categorical", "第5层：影响力第一的政权", 2},
	{"top2", "重叠政权", "", "政治", "categorical", "第5层：影响力第二的政权", 2},
	{"share", "控制强度", "", "政治", "continuous", "第一名影响力占前两名之和的比例", 1},
	{"depth", "统治层级", "级", "社会", "continuous", "格内家族到最高首领的平均层级", 1},
	{"t_farm", "农耕技能", "", "属性", "continuous", "格内平均", 1},
	{"t_herd", "放牧技能", "", "属性", "continuous", "格内平均", 1},
	{"t_hunt", "狩猎技能", "", "属性", "continuous", "格内平均", 1},
	{"t_aggr", "好战", "", "属性", "continuous", "格内平均", 1},
	{"t_mob", "流动性", "", "属性", "continuous", "格内平均", 1},
	{"t_loyal", "忠诚", "", "属性", "continuous", "格内平均", 1},
	{"hate", "仇恨热度", "", "社会", "continuous", "格内家族仇恨值均值", 1},
	{"clim", "气候波动", "", "人口", "continuous", "产出乘数(0.4-1.6)", 1},
}

type FramePolity struct {
	UID      int64
	Pop      float32
	Families uint32
	Cells    uint32
	Capital  int32
	Power    float32
}

const framePolityBytes = 8 + 4 + 4 + 4 + 4 + 4

// PolScattered 表示"零散家族"(家族数 < MinFramePolity 的小政权)，0 表示无。
const PolScattered = 65535
const MinFramePolity = 3

// SlowFields 变化缓慢，只在每 SlowEvery 年的帧中刷新(其余帧沿用上一帧，压缩后几乎为零)。
var SlowFields = map[string]bool{"t_farm": true, "t_herd": true, "t_hunt": true, "t_aggr": true,
	"t_mob": true, "t_loyal": true, "hate": true}

const SlowEvery = 25

// FieldOffset 返回帧中某字段的字节偏移与长度(n = 陆地格数)。
func FieldOffset(id string, n int) (int, int) {
	off := 12
	for _, f := range FrameFields {
		if f.ID == id {
			return off, f.Bytes * n
		}
		off += f.Bytes * n
	}
	return -1, 0
}

// BuildFrame 汇总当前状态为每个陆地格的动态字段 + 本帧政权表。返回编码后的字节。
// 格子顺序 = World.LandIdx。
func (s *State) BuildFrame(year int) []byte {
	w := s.W
	f := &s.F
	N := w.N
	pop := make([]float32, N)
	fam := make([]int32, N)
	var tr [6][]float32
	for k := range tr {
		tr[k] = make([]float32, N)
	}
	traitIdx := [6]int{TFarm, THerd, THunt, TAggr, TMob, TLoyal}
	skills := make([][4]float32, N)
	depth := make([]float32, N)
	hate := make([]float32, N)
	tribeSize := map[uint64]float32{}
	for i := 0; i < f.Len(); i++ {
		c := f.Cell[i]
		sz := f.Size[i]
		pop[c] += sz
		fam[c]++
		for k, t := range traitIdx {
			tr[k][c] += sz * f.Tr[t][i]
		}
		for t := 0; t < 4; t++ {
			skills[c][t] += sz * f.Tr[t][i]
		}
		depth[c] += float32(f.Depth[i])
		for g := 0; g < NGrudge; g++ {
			hate[c] += f.GrV[g][i]
		}
		tribeSize[cellRootKey(c, f.Root[i])] += sz
	}
	// 每格人口最多的最高首领
	tribe := make([]int64, N)
	best := make([]float32, N)
	keys := make([]uint64, 0, len(tribeSize))
	for k := range tribeSize {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	for _, k := range keys {
		c := int32(k >> 32)
		v := tribeSize[k]
		if v > best[c] {
			best[c] = v
			tribe[c] = f.UID[int32(uint32(k))]
		}
	}
	// 政权表：帧内引用到的、有一定规模的政权
	nfam := map[int64]int{}
	for i := 0; i < f.Len(); i++ {
		nfam[f.UID[f.Root[i]]]++
	}
	polIdx := map[int64]uint16{}
	var pols []FramePolity
	ref := func(uid int64) uint16 {
		if uid == 0 {
			return 0
		}
		if nfam[uid] < MinFramePolity {
			return PolScattered
		}
		if k, ok := polIdx[uid]; ok {
			return k
		}
		if len(pols) >= 65533 {
			return 0
		}
		pols = append(pols, FramePolity{UID: uid})
		polIdx[uid] = uint16(len(pols))
		return uint16(len(pols))
	}
	inf := s.Inf
	hasInf := len(inf.Top1) == N
	L := w.LandIdx
	buf := make([]byte, 0, len(L)*21+16)
	buf = append(buf, 'M', 'S', 'F', '1')
	buf = binary.LittleEndian.AppendUint32(buf, uint32(year))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(L)))
	put16 := func(v uint16) { buf = binary.LittleEndian.AppendUint16(buf, v) }
	// pop
	S := s.PeoplePerUnit
	for _, c := range L {
		v := math.Log10(1+float64(pop[c]*S)) * 10000
		put16(uint16(math.Min(v, 65535)))
	}
	for _, c := range L {
		put16(uint16(min(fam[c], 65535)))
	}
	for _, c := range L {
		buf = append(buf, s.dynMode(int(c), skills[c], pop[c]))
	}
	for _, c := range L {
		put16(ref(tribe[c]))
	}
	for _, c := range L {
		if hasInf {
			put16(ref(inf.Top1[c]))
		} else {
			put16(0)
		}
	}
	for _, c := range L {
		if hasInf {
			put16(ref(inf.Top2[c]))
		} else {
			put16(0)
		}
	}
	for _, c := range L {
		var sh float64 = 1
		if hasInf && inf.Top2[c] != 0 {
			sh = 1 / (1 + math.Exp(float64(inf.V2[c]-inf.V1[c])))
		}
		buf = append(buf, uint8(sh*255))
	}
	for _, c := range L {
		d := float32(0)
		if fam[c] > 0 {
			d = depth[c] / float32(fam[c])
		}
		buf = append(buf, uint8(clamp(d*20, 0, 255)))
	}
	for k := range tr {
		for _, c := range L {
			v := float32(0)
			if pop[c] > 0 {
				v = tr[k][c] / pop[c]
			}
			buf = append(buf, uint8(clamp(v*255, 0, 255)))
		}
	}
	for _, c := range L {
		v := float32(0)
		if fam[c] > 0 {
			v = hate[c] / float32(fam[c])
		}
		buf = append(buf, uint8(clamp(v/2*255, 0, 255)))
	}
	for _, c := range L {
		buf = append(buf, uint8(clamp((s.ClimA[c]-0.4)/1.2*255, 0, 255)))
	}
	// 政权表统计
	agg := map[int64]*FramePolity{}
	for k := range pols {
		agg[pols[k].UID] = &pols[k]
	}
	for i := 0; i < f.Len(); i++ {
		if p := agg[f.UID[f.Root[i]]]; p != nil {
			p.Pop += f.Size[i] * S
			p.Families++
			p.Power += s.power(i)
		}
		if f.Parent[i] < 0 {
			if p := agg[f.UID[i]]; p != nil {
				p.Capital = f.Cell[i]
			}
		}
	}
	if hasInf {
		for _, c := range w.LandIdx {
			if p := agg[inf.Top1[c]]; p != nil {
				p.Cells++
			}
		}
	}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(pols)))
	for _, p := range pols {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(p.UID))
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(p.Pop))
		buf = binary.LittleEndian.AppendUint32(buf, p.Families)
		buf = binary.LittleEndian.AppendUint32(buf, p.Cells)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(p.Capital))
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(p.Power))
	}
	return buf
}

// FrameCellBytes 是帧中格子数据部分的字节数(用于差分压缩只对齐这一段)。
func FrameCellBytes(n int) int {
	t := 0
	for _, f := range FrameFields {
		t += f.Bytes
	}
	return 12 + t*n
}

func DecodeFramePolities(b []byte, n int) ([]FramePolity, error) {
	off := FrameCellBytes(n)
	if len(b) < off+4 {
		return nil, fmt.Errorf("帧太短")
	}
	k := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	out := make([]FramePolity, k)
	for i := range out {
		if len(b) < off+framePolityBytes {
			return nil, fmt.Errorf("政权表截断")
		}
		out[i] = FramePolity{
			UID:      int64(binary.LittleEndian.Uint64(b[off:])),
			Pop:      math.Float32frombits(binary.LittleEndian.Uint32(b[off+8:])),
			Families: binary.LittleEndian.Uint32(b[off+12:]),
			Cells:    binary.LittleEndian.Uint32(b[off+16:]),
			Capital:  int32(binary.LittleEndian.Uint32(b[off+20:])),
			Power:    math.Float32frombits(binary.LittleEndian.Uint32(b[off+24:])),
		}
		off += framePolityBytes
	}
	return out, nil
}

// dynMode：由格内技能均值 + 格子环境推出生计方式标签(与第2层编码一致)。
func (s *State) dynMode(c int, sk [4]float32, pop float32) uint8 {
	if pop <= 0 {
		return 0
	}
	w := s.W
	best := dominantSkill(sk)
	switch best {
	case TFarm:
		if sk[THerd] > 0.6*sk[TFarm] {
			return 4
		}
		if w.F["cap_rice"][c] >= w.F["cap_dry"][c] && w.F["cap_rice"][c] >= w.F["cap_irr"][c] {
			return 1
		}
		if w.F["cap_irr"][c] > w.F["cap_dry"][c] {
			return 3
		}
		return 2
	case THerd:
		if w.F["elev"][c] >= 3000 {
			return 7
		}
		if w.F["cap_rein"][c] > 0 {
			return 8
		}
		if w.F["mi"][c] < 0.15 {
			return 6
		}
		return 5
	case THunt:
		return 9
	}
	return 10
}
