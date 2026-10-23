package sim

// 随机超级帝国：模拟历史上突然崛起、疆域极其辽阔的大帝国(如蒙古帝国)。
//
// 崛起：每 5 年以 SuperRate 的概率，从中等以上规模的政权中挑一个
//       (偏好好战、高流动、善骑的游牧政权)，获得实力与投送距离的超常加成。
// 鼎盛：持续 SuperYearsMin~SuperYearsMax 年。期间影响范围可远超普通上限，
//       一次可同时发动多场战争，内部叛乱被强力压制。
// 衰落：期满或首领被吞并即结束，加成消失并强制爆发继承危机，帝国分裂为若干汗国。

type SuperInfo struct {
	Start, End int
	PowerMul   float32
	ReachMul   float32
	Share      float32 // 当前统治人口占比，越高加成越弱(饱和)
}

// superMul 返回首领下标 root 所在政权的实力倍数与投送倍数(普通政权为 1)。
func (s *State) superMul(root int32) (float32, float32) {
	if len(s.Super) == 0 || root < 0 {
		return 1, 1
	}
	if sp, ok := s.Super[s.F.UID[root]]; ok {
		// 统治人口占比接近 SuperCap 时实力加成消退：超级帝国疆域辽阔，但不会吞并整个大陆
		t := clamp(1-sp.Share/s.P.SuperCap, 0, 1)
		return 1 + (sp.PowerMul-1)*t, 1 + (sp.ReachMul-1)*t
	}
	return 1, 1
}

// superStep 在政权步骤开始时调用：结束到期的超级帝国，并按概率产生新的。
func (s *State) superStep(ps []polityAgg) {
	r := s.rng(phaseSuper, 0)
	alive := map[int64]int{}
	var total float32
	for k := range ps {
		alive[ps[k].uid] = k
		total += ps[k].size
	}
	// 按 UID 排序处理，保证确定性
	var uids []int64
	for u := range s.Super {
		uids = append(uids, u)
	}
	sortInt64(uids)
	for _, u := range uids {
		sp := s.Super[u]
		k, ok := alive[u]
		if ok && s.Year < sp.End {
			sp.Share = ps[k].size / (total + 1e-6)
			s.Super[u] = sp
			continue
		}
		delete(s.Super, u)
		if pi := s.Polities[u]; pi != nil {
			pi.SuperEnd = s.Year
		}
		if !ok {
			s.event("super_fall", u, 0)
			continue
		}
		// 衰落：直属下级大批分裂出去
		split := 0
		root := ps[k].root
		for i := 0; i < s.F.Len(); i++ {
			if s.F.Parent[i] != root {
				continue
			}
			// 被吞并的旧政权首领几乎都会复国；本族分支按忠诚度分裂
			pr := 0.35 + 0.5*(1-s.F.Tr[TLoyal][i])
			if pi := s.Polities[s.F.UID[i]]; pi != nil && pi.AbsorbedBy == u {
				pr = 0.9
			}
			if r.Float32() < pr {
				s.F.Parent[i] = -1
				s.Hatred[hkey(u, s.F.UID[i])] += 0.5
				split++
			}
		}
		s.event("super_fall", u, 0, split)
	}
	if s.P.SuperRate <= 0 || len(s.Super) >= s.P.SuperMaxActive || r.Float32() >= s.P.SuperRate {
		return
	}
	// 候选：至少 20 个家族；权重偏好好战 × 机动善骑 × 规模
	var cand []int
	var wts []float32
	var tot float32
	for k := range ps {
		p := &ps[k]
		if p.nfam < 20 {
			continue
		}
		if _, ok := s.Super[p.uid]; ok {
			continue
		}
		wt := float32(p.nfam) * (0.3 + p.aggr) * (0.3 + 2*p.herd*p.mob)
		cand = append(cand, k)
		wts = append(wts, wt)
		tot += wt
	}
	if tot <= 0 {
		return
	}
	x := r.Float32() * tot
	pick := cand[len(cand)-1]
	for j, wv := range wts {
		if x < wv {
			pick = cand[j]
			break
		}
		x -= wv
	}
	p := &ps[pick]
	dur := s.P.SuperYearsMin + r.IntN(max(1, s.P.SuperYearsMax-s.P.SuperYearsMin+1))
	sp := SuperInfo{Start: s.Year, End: s.Year + dur,
		PowerMul: s.P.SuperPower * (0.8 + 0.4*r.Float32()),
		ReachMul: s.P.SuperReach * (0.8 + 0.4*r.Float32())}
	if s.Super == nil {
		s.Super = map[int64]SuperInfo{}
	}
	s.Super[p.uid] = sp
	pi := s.Polities[p.uid]
	if pi == nil {
		pi = &PolityInfo{UID: p.uid, Name: polityName(p.uid), Born: s.Year}
		s.Polities[p.uid] = pi
	}
	pi.SuperStart, pi.SuperEnd = sp.Start, sp.End
	s.event("super_rise", p.uid, 0, dur)
}

func sortInt64(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
