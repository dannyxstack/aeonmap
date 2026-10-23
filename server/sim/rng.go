package sim

import (
	"math"
	"math/rand/v2"
	"sync"
)

// 每个阶段、每个数据块都有自己的确定性随机源：PCG(种子⊕年份⊕阶段, 块号)。
// 这样结果与 goroutine 调度无关，回放时从检查点重算能得到完全相同的历史。
const chunkSize = 4096

const (
	phaseInit = iota + 1
	phaseProduce
	phaseDemog
	phaseSplit
	phaseMigrate
	phaseCulture
	phaseConquest
	phaseRebel
	phasePolity
	phaseWar
	phaseSucc
	phaseDefect
	phaseClimate
	phaseSuper
)

func mix(a, b uint64) uint64 {
	x := a ^ (b + 0x9e3779b97f4a7c15 + (a << 6) + (a >> 2))
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

func (s *State) rng(phase int, chunk int) *rand.Rand {
	k := mix(mix(s.P.Seed, uint64(s.Year)), uint64(phase))
	// 两个种子都要充分混合：第二个种子为 0 时 PCG 的首个输出分布很差
	return rand.New(rand.NewPCG(k, mix(k, uint64(chunk)+1)))
}

// parallelFor 按固定块大小切分 [0,n)，块内顺序执行，块之间并行。
func parallelFor(n int, fn func(start, end, chunk int)) {
	chunks := (n + chunkSize - 1) / chunkSize
	if chunks <= 1 {
		if n > 0 {
			fn(0, n, 0)
		}
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for c := 0; c < chunks; c++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(c int) {
			defer wg.Done()
			s := c * chunkSize
			e := s + chunkSize
			if e > n {
				e = n
			}
			fn(s, e, c)
			<-sem
		}(c)
	}
	wg.Wait()
}

var workers = 8

func SetWorkers(n int) {
	if n > 0 {
		workers = n
	}
}

func clamp(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func exp32(x float32) float32 { return float32(math.Exp(float64(x))) }
func log32(x float32) float32 { return float32(math.Log(float64(x))) }
