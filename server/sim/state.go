// Package sim 实现第3-5层：家族繁衍与迁移、征服与统治关系网、政权与战争。
package sim

import (
	"math"
	"sort"

	"mapsim/world"
)

// 遗传属性(0..1，生育力 0.5..1.5)
const (
	TFarm = iota
	THerd
	THunt
	TFish
	TAggr
	TMob
	TLoyal
	TFert
	NTraits
)

var TraitNames = [NTraits]string{"农耕", "放牧", "狩猎", "捕鱼", "好战", "流动", "忠诚", "生育"}

const NGrudge = 4

// Families 按字段分数组存储(SoA)。下标会在压缩时变化，UID 永不复用。
type Families struct {
	UID    []int64
	Cell   []int32
	Parent []int32 // 上级家族下标，-1 表示最高首领
	Size   []float32
	Store  []float32
	Food   []float32 // 上一年产出
	Born   []int32   // 建立年份
	Tr     [NTraits][]float32
	GrT    [NGrudge][]int64 // 仇恨对象：政权 UID(=其最高首领家族 UID)
	GrV    [NGrudge][]float32
	// 每步重算，不需要存档(但存档也无害)
	Root  []int32
	Depth []int32
	Dead  []bool
}

func (f *Families) Len() int { return len(f.UID) }

func (f *Families) appendFrom(src *Families, i int) {
	f.UID = append(f.UID, src.UID[i])
	f.Cell = append(f.Cell, src.Cell[i])
	f.Parent = append(f.Parent, src.Parent[i])
	f.Size = append(f.Size, src.Size[i])
	f.Store = append(f.Store, src.Store[i])
	f.Food = append(f.Food, src.Food[i])
	f.Born = append(f.Born, src.Born[i])
	for t := 0; t < NTraits; t++ {
		f.Tr[t] = append(f.Tr[t], src.Tr[t][i])
	}
	for g := 0; g < NGrudge; g++ {
		f.GrT[g] = append(f.GrT[g], src.GrT[g][i])
		f.GrV[g] = append(f.GrV[g], src.GrV[g][i])
	}
	f.Root = append(f.Root, src.Root[i])
	f.Depth = append(f.Depth, src.Depth[i])
	f.Dead = append(f.Dead, false)
}

// PolityInfo 是政权的历史记录(UID = 首领家族 UID)。
type PolityInfo struct {
	UID        int64   `json:"uid"`
	Name       string  `json:"name"`
	Born       int     `json:"born"`
	Died       int     `json:"died"`                 // 0 表示仍存在
	SuperStart int     `json:"superStart,omitempty"` // 超级帝国鼎盛期(0 表示从未)
	SuperEnd   int     `json:"superEnd,omitempty"`
	Custom     bool    `json:"custom,omitempty"` // 用户约束指定的政权(名字由用户给出)
	PeakPop    float64 `json:"peakPop"`
	PeakCells  int     `json:"peakCells"`
	Capital    int32   `json:"capital"`
	Mode       int     `json:"mode"`
	AbsorbedBy int64   `json:"absorbedBy,omitempty"`
}

type Counters struct {
	Births, Splits, Deaths, Moves                 int
	Conquests, ConquestFails, Rebellions, Defects int
	Wars, Annexations, WarFails, SuccessionSplits int
}

type State struct {
	P             Params
	Year          int
	NextUID       int64
	PeoplePerUnit float32
	F             Families
	// 气候波动：每格产出乘数，及其粗网格 AR(1) 状态
	ClimA []float32
	ClimC []float32
	// 政权层
	Hatred   map[[2]int64]float32 // 政权对之间的仇恨(键：小UID,大UID)
	Super    map[int64]SuperInfo  // 正处于鼎盛期的超级帝国
	Cons     []ConsPolity         // 用户约束的政权
	Polities map[int64]*PolityInfo
	Inf      Influence
	Cnt      Counters
	TotalCnt Counters

	Events []Event // 待写出的政权层事件

	W *world.World `json:"-"`
	// 工作缓冲(不存档)
	buf       *buffers
	lockOwner []int16 // 每格的锁定归属(约束政权序号，-1 不锁定)
	infIdx    []int32 // 影响力场第一/第二名(本步政权下标)
	infIdx2   []int32
}

type Event struct {
	Year int    `json:"year"`
	Type string `json:"type"`
	A    int64  `json:"a"`
	B    int64  `json:"b,omitempty"`
	N    int    `json:"n,omitempty"`
}

func (s *State) event(typ string, a, b int64, n ...int) {
	e := Event{Year: s.Year, Type: typ, A: a, B: b}
	if len(n) > 0 {
		e.N = n[0]
	}
	s.Events = append(s.Events, e)
}

type buffers struct {
	cellStart []int32 // 按格分桶：families 按 cell 排序后的起始下标
	order     []int32
	effort    [4][]float32
	ret       [4][]float32
	rootPow   map[uint64]float32
	rootSize  map[uint64]float32
	cellSize  []float32
}

// NewState 按第2层的承载力在陆地上撒初始家族。
func NewState(w *world.World, p Params) *State { return NewStateWith(w, p, nil) }

// NewStateWith 在初始化家族后应用用户约束(指定首都与领土)。
func NewStateWith(w *world.World, p Params, cons *Constraints) *State {
	s := &State{P: p, W: w, Hatred: map[[2]int64]float32{}, Polities: map[int64]*PolityInfo{}}
	s.ClimA = make([]float32, w.N)
	for i := range s.ClimA {
		s.ClimA[i] = 1
	}
	// 标定人口单位：均衡时约 TargetFamily 个家族，平均规模约 0.65*SplitSize，
	// 均衡人口约为承载力的 0.7
	var ktot float64
	dens := w.F["density"]
	for _, c := range w.LandIdx {
		ktot += float64(dens[c] * w.Area[c])
	}
	s.PeoplePerUnit = float32(ktot / (float64(p.TargetFamily) * 0.65 * float64(p.SplitSize) / 0.7))

	r := s.rng(phaseInit, 0)
	// 按承载力加权抽样初始家族所在格
	cum := make([]float64, len(w.LandIdx))
	var acc float64
	for k, c := range w.LandIdx {
		acc += math.Sqrt(float64(dens[c]*w.Area[c])) + 1e-6 // 开方：让稀疏地区也有人
		cum[k] = acc
	}
	initSize := float32(ktot) / s.PeoplePerUnit * 0.35 / float32(p.InitFamilies)
	if initSize < 3 {
		initSize = 3
	}
	f := &s.F
	for n := 0; n < p.InitFamilies; n++ {
		x := r.Float64() * acc
		k := sort.SearchFloat64s(cum, x)
		if k >= len(cum) {
			k = len(cum) - 1
		}
		c := w.LandIdx[k]
		s.NextUID++
		f.UID = append(f.UID, s.NextUID)
		f.Cell = append(f.Cell, c)
		f.Parent = append(f.Parent, -1)
		f.Size = append(f.Size, initSize*(0.6+0.8*r.Float32()))
		f.Store = append(f.Store, initSize*0.3)
		f.Food = append(f.Food, 0)
		f.Born = append(f.Born, 0)
		sk := modeSkills(w.Mode[c])
		for t := 0; t < 4; t++ {
			f.Tr[t] = append(f.Tr[t], clamp(sk[t]+0.15*float32(r.NormFloat64()), 0.02, 1))
		}
		f.Tr[TAggr] = append(f.Tr[TAggr], clamp(0.35+0.15*float32(r.NormFloat64()), 0, 1))
		mob := float32(0.2)
		if sk[THerd] > 0.6 {
			mob = 0.7
		} else if sk[THunt] > 0.6 {
			mob = 0.5
		}
		f.Tr[TMob] = append(f.Tr[TMob], clamp(mob+0.1*float32(r.NormFloat64()), 0, 1))
		f.Tr[TLoyal] = append(f.Tr[TLoyal], clamp(0.6+0.15*float32(r.NormFloat64()), 0, 1))
		f.Tr[TFert] = append(f.Tr[TFert], clamp(1+0.1*float32(r.NormFloat64()), 0.5, 1.5))
		for g := 0; g < NGrudge; g++ {
			f.GrT[g] = append(f.GrT[g], 0)
			f.GrV[g] = append(f.GrV[g], 0)
		}
		f.Root = append(f.Root, int32(n))
		f.Depth = append(f.Depth, 0)
		f.Dead = append(f.Dead, false)
	}
	s.computeRoots()
	s.applyConstraints(cons)
	return s
}

// modeSkills：第2层生计方式 → 初始四项技能(农、牧、猎、渔)
func modeSkills(m uint8) [4]float32 {
	switch m {
	case 1, 2, 3: // 水稻、旱作、灌溉
		return [4]float32{0.85, 0.15, 0.1, 0.15}
	case 4: // 农牧混合
		return [4]float32{0.55, 0.55, 0.15, 0.1}
	case 5, 6, 7, 8: // 游牧、驯鹿
		return [4]float32{0.1, 0.85, 0.25, 0.05}
	case 9:
		return [4]float32{0.05, 0.1, 0.85, 0.2}
	case 10:
		return [4]float32{0.1, 0.05, 0.3, 0.85}
	}
	return [4]float32{0.3, 0.3, 0.3, 0.3}
}

// compact 移除死亡家族并重映射 Parent。死者的下级改挂到死者的上级。
func (s *State) compact() {
	f := &s.F
	n := f.Len()
	// 先把死者的下级上移(可能多层死亡，迭代到稳定)
	for iter := 0; iter < 32; iter++ {
		changed := false
		for i := 0; i < n; i++ {
			p := f.Parent[i]
			if p >= 0 && f.Dead[p] {
				f.Parent[i] = f.Parent[p]
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	remap := make([]int32, n)
	var nf Families
	k := int32(0)
	for i := 0; i < n; i++ {
		if f.Dead[i] {
			remap[i] = -1
			continue
		}
		remap[i] = k
		k++
	}
	if int(k) == n {
		return
	}
	for i := 0; i < n; i++ {
		if !f.Dead[i] {
			nf.appendFrom(f, i)
		}
	}
	for i := range nf.Parent {
		if p := nf.Parent[i]; p >= 0 {
			nf.Parent[i] = remap[p]
		}
	}
	s.F = nf
}

// computeRoots 用指针跳跃求每个家族的最高首领与层级深度；检测并打断环。
func (s *State) computeRoots() int {
	f := &s.F
	n := f.Len()
	if cap(f.Root) < n {
		f.Root = make([]int32, n)
		f.Depth = make([]int32, n)
	}
	f.Root = f.Root[:n]
	f.Depth = f.Depth[:n]
	ptr := make([]int32, n)
	dep := make([]int32, n)
	for i := 0; i < n; i++ {
		if f.Parent[i] < 0 {
			ptr[i] = int32(i)
			dep[i] = 0
		} else {
			ptr[i] = f.Parent[i]
			dep[i] = 1
		}
	}
	nptr := make([]int32, n)
	ndep := make([]int32, n)
	broken := 0
	for iter := 0; iter < 40; iter++ {
		done := true
		for i := 0; i < n; i++ {
			p := ptr[i]
			pp := ptr[p]
			if pp != p {
				done = false
			}
			nptr[i] = pp
			ndep[i] = dep[i] + dep[p] // 首领 dep=0，自指时加 0
		}
		ptr, nptr = nptr, ptr
		dep, ndep = ndep, dep
		if done {
			break
		}
		if iter == 39 {
			// 仍未收敛 → 存在环：把环上的家族全部设为独立
			for i := 0; i < n; i++ {
				if ptr[ptr[i]] != ptr[i] {
					f.Parent[i] = -1
					broken++
				}
			}
			return broken + s.computeRoots()
		}
	}
	copy(f.Root, ptr)
	copy(f.Depth, dep)
	return broken
}

func hkey(a, b int64) [2]int64 {
	if a < b {
		return [2]int64{a, b}
	}
	return [2]int64{b, a}
}

// addGrudge 给家族 i 增加对政权 target 的仇恨(4 槽位，替换最弱)。
func (s *State) addGrudge(i int, target int64, v float32) {
	f := &s.F
	weak := 0
	for g := 0; g < NGrudge; g++ {
		if f.GrT[g][i] == target {
			f.GrV[g][i] += v
			return
		}
		if f.GrV[g][i] < f.GrV[weak][i] {
			weak = g
		}
	}
	if f.GrV[weak][i] < v {
		f.GrT[weak][i] = target
		f.GrV[weak][i] = v
	}
}

func (s *State) grudge(i int, target int64) float32 {
	f := &s.F
	for g := 0; g < NGrudge; g++ {
		if f.GrT[g][i] == target {
			return f.GrV[g][i]
		}
	}
	return 0
}

func (s *State) power(i int) float32 {
	f := &s.F
	st := clamp(f.Store[i]/(f.Size[i]+1e-3), 0, 1)
	return f.Size[i] * (0.5 + f.Tr[TAggr][i]) * (1 + 0.6*f.Tr[THerd][i]*f.Tr[TMob][i]) * (1 + 0.3*st)
}

// powerEff 是计入所属政权超级帝国加成后的实力。
func (s *State) powerEff(i int) float32 {
	m, _ := s.superMul(s.F.Root[i])
	return s.power(i) * m
}
