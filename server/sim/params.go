package sim

// Params 是一次演化的全部参数。演化开始后不可修改(保证回放可复现)。
type Params struct {
	Seed           uint64  `json:"seed" group:"初始" desc:"随机种子；同样参数+种子得到同样历史" min:"1" max:"999999"`
	Years          int     `json:"years" group:"初始" desc:"演化年数" min:"100" max:"10000"`
	InitFamilies   int     `json:"initFamilies" group:"初始" desc:"初始家族数" min:"1000" max:"100000"`
	MaxFamilies    int     `json:"maxFamilies" group:"初始" desc:"家族数硬上限(达到后停止分家)" min:"1000" max:"200000"`
	TargetFamily   int     `json:"targetFamilies" group:"初始" desc:"标定人口单位时的目标均衡家族数" min:"1000" max:"200000"`
	SplitSize      float32 `json:"splitSize" group:"繁衍" desc:"家族规模达到此值时分家(单位)" min:"6" max:"100"`
	BirthRate      float32 `json:"birthRate" group:"繁衍" desc:"资源充足时的年出生率" min:"0" max:"0.1"`
	DeathRate      float32 `json:"deathRate" group:"繁衍" desc:"基础年死亡率" min:"0" max:"0.1"`
	Productivity   float32 `json:"productivity" group:"繁衍" desc:"不拥挤时每单位劳力的产出倍数" min:"1" max:"4"`
	Mutation       float32 `json:"mutation" group:"繁衍" desc:"分家时属性变异的标准差" min:"0" max:"0.2"`
	Culture        float32 `json:"culture" group:"繁衍" desc:"每年向本格成功者学习生计技能的速率" min:"0" max:"0.3"`
	MoveRate       float32 `json:"moveRate" group:"迁移" desc:"每年考虑迁移的基础概率" min:"0" max:"1"`
	ClimateNoise   float32 `json:"climateNoise" group:"迁移" desc:"气候波动幅度(产出乘数的标准差)" min:"0" max:"0.5"`
	ConquestRate   float32 `json:"conquestRate" group:"征服" desc:"资源达阈值的家族每年尝试征服的概率系数" min:"0" max:"1"`
	ConquestThr    float32 `json:"conquestThr" group:"征服" desc:"尝试征服所需的存粮/人口比" min:"0" max:"3"`
	TributeRate    float32 `json:"tributeRate" group:"征服" desc:"下级每年上交剩余的比例" min:"0" max:"0.8"`
	GrudgeDecay    float32 `json:"grudgeDecay" group:"征服" desc:"仇恨每年衰减后保留的比例" min:"0.5" max:"1"`
	RebelRate      float32 `json:"rebelRate" group:"征服" desc:"叛乱基础概率系数" min:"0" max:"0.5"`
	MaxDepth       int     `json:"maxDepth" group:"征服" desc:"分家产生的统治层级上限" min:"2" max:"20"`
	ReachKm        float32 `json:"reachKm" group:"政权" desc:"政权影响力衰减长度基准(km，按通行阻力折算)" min:"50" max:"3000"`
	MaxReachFrac   float32 `json:"maxReachFrac" group:"政权" desc:"征服/影响的最远距离占地图宽度的比例" min:"0.05" max:"1"`
	WarRate        float32 `json:"warRate" group:"政权" desc:"接壤政权每5年开战的概率系数" min:"0" max:"1"`
	SuccCrisis     float32 `json:"succCrisis" group:"政权" desc:"首领更替时爆发继承危机的概率系数" min:"0" max:"1"`
	DefectRate     float32 `json:"defectRate" group:"政权" desc:"处于他国势力范围内的家族倒戈概率" min:"0" max:"1"`
	SuperRate      float32 `json:"superRate" group:"超级帝国" desc:"每5年随机崛起一个超级帝国的概率(0=关闭)" min:"0" max:"1"`
	SuperPower     float32 `json:"superPower" group:"超级帝国" desc:"超级帝国的实力倍数" min:"1" max:"20"`
	SuperReach     float32 `json:"superReach" group:"超级帝国" desc:"超级帝国的投送距离倍数(也放宽最远距离上限)" min:"1" max:"10"`
	SuperYearsMin  int     `json:"superYearsMin" group:"超级帝国" desc:"鼎盛期最短年数" min:"10" max:"1000"`
	SuperYearsMax  int     `json:"superYearsMax" group:"超级帝国" desc:"鼎盛期最长年数" min:"10" max:"1000"`
	SuperMaxActive int     `json:"superMaxActive" group:"超级帝国" desc:"同时存在的超级帝国上限" min:"1" max:"10"`
	SuperCap       float32 `json:"superCap" group:"超级帝国" desc:"统治人口占比达到此值时实力加成完全消退" min:"0.05" max:"1"`
}

func DefaultParams() Params {
	return Params{
		Seed:           20261015,
		Years:          4000,
		InitFamilies:   50000,
		MaxFamilies:    100000,
		TargetFamily:   65000,
		SplitSize:      30,
		BirthRate:      0.03,
		DeathRate:      0.012,
		Productivity:   2.0,
		Mutation:       0.03,
		Culture:        0.04,
		MoveRate:       0.08,
		ClimateNoise:   0.15,
		ConquestRate:   0.06,
		ConquestThr:    0.4,
		TributeRate:    0.25,
		GrudgeDecay:    0.97,
		RebelRate:      0.03,
		MaxDepth:       6,
		ReachKm:        350,
		MaxReachFrac:   0.2,
		WarRate:        0.25,
		SuccCrisis:     0.5,
		DefectRate:     0.1,
		SuperRate:      0.012, // 平均约 400 年一个
		SuperPower:     3,
		SuperReach:     3,
		SuperYearsMin:  80,
		SuperYearsMax:  200,
		SuperMaxActive: 2,
		SuperCap:       0.3,
	}
}
