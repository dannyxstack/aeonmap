package sim

import "testing"

func TestSuperRateDraws(t *testing.T) {
	s := &State{P: DefaultParams()}
	n := 0
	for y := 0; y < 4000; y += 5 {
		s.Year = y
		if s.rng(phaseSuper, 0).Float32() < s.P.SuperRate {
			n++
		}
	}
	// 期望约 9.6 次；回归检查随机数派生的首个输出不再严重偏低
	if n < 3 || n > 20 {
		t.Fatalf("首个随机数分布异常：%d / 800 低于 %.3f", n, s.P.SuperRate)
	}
}

func TestFirstDrawUniform(t *testing.T) {
	s := &State{P: DefaultParams()}
	var buckets [10]int
	for y := 0; y < 20000; y++ {
		s.Year = y
		buckets[int(s.rng(phaseWar, 0).Float32()*10)]++
	}
	for k, c := range buckets {
		if c < 1700 || c > 2300 {
			t.Fatalf("第 %d 档 %d 次(期望约 2000)", k, c)
		}
	}
}
