// Package ai：调用多模态大模型对上传的地图做初步解析(判断类型、读图例、找经纬度范围)，
// 输出可直接填入 POST /v1/worlds 的建议参数。像素级的高程反推仍由预处理完成，结果可复现。
package ai

// Provider 描述一家服务商。Kind=anthropic 走 Claude 官方 SDK；Kind=openai 走 OpenAI 兼容的
// /chat/completions 接口(国内主流服务商都提供)。
type Provider struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	BaseURL string   `json:"baseURL"`
	Models  []string `json:"models"` // 第一个为默认模型；管理后台也可以手填其他模型名
	KeyURL  string   `json:"keyURL"` // 申请密钥的控制台
	Note    string   `json:"note,omitempty"`
}

// Providers 是内置的服务商清单。模型名会随厂商更新，管理后台可直接手填新的模型名。
var Providers = []Provider{
	{ID: "anthropic", Label: "Claude(Anthropic)", Kind: "anthropic", BaseURL: "https://api.anthropic.com",
		Models: []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"},
		KeyURL: "https://console.anthropic.com/settings/keys",
		Note:   "海外服务，中国大陆服务器需能访问 api.anthropic.com，或在接口地址里填代理/网关地址"},
	{ID: "qwen", Label: "通义千问(阿里云百炼)", Kind: "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models: []string{"qwen3-vl-plus", "qwen-vl-max", "qwen-vl-plus", "qwen3-vl-flash"},
		KeyURL: "https://bailian.console.aliyun.com/?apiKey=1",
		Note:   "国际站地址为 https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
	{ID: "doubao", Label: "豆包(火山方舟)", Kind: "openai", BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Models: []string{"doubao-seed-1-6-vision-250815", "doubao-1-5-vision-pro-32k-250115"},
		KeyURL: "https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey",
		Note:   "模型处也可以填推理接入点 ID(ep-…)"},
	{ID: "zhipu", Label: "智谱 GLM", Kind: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Models: []string{"glm-4.5v", "glm-4v-plus-0111", "glm-4v-flash"},
		KeyURL: "https://open.bigmodel.cn/usercenter/apikeys"},
	{ID: "moonshot", Label: "Kimi(月之暗面)", Kind: "openai", BaseURL: "https://api.moonshot.cn/v1",
		Models: []string{"moonshot-v1-32k-vision-preview", "kimi-latest"},
		KeyURL: "https://platform.moonshot.cn/console/api-keys"},
	{ID: "hunyuan", Label: "腾讯混元", Kind: "openai", BaseURL: "https://api.hunyuan.cloud.tencent.com/v1",
		Models: []string{"hunyuan-turbos-vision", "hunyuan-vision"},
		KeyURL: "https://console.cloud.tencent.com/hunyuan/api-key"},
	{ID: "qianfan", Label: "文心(百度千帆)", Kind: "openai", BaseURL: "https://qianfan.baidubce.com/v2",
		Models: []string{"ernie-4.5-turbo-vl-32k", "ernie-4.5-vl-28b-a3b"},
		KeyURL: "https://console.bce.baidu.com/iam/#/iam/apikey/list"},
	{ID: "siliconflow", Label: "硅基流动(开源模型托管)", Kind: "openai", BaseURL: "https://api.siliconflow.cn/v1",
		Models: []string{"Qwen/Qwen2.5-VL-72B-Instruct", "zai-org/GLM-4.5V"},
		KeyURL: "https://cloud.siliconflow.cn/account/ak"},
	{ID: "custom", Label: "自定义(OpenAI 兼容，如自建 vLLM/Ollama)", Kind: "openai", BaseURL: "",
		Models: []string{}, Note: "填写完整的接口地址(到 /v1 为止)与模型名；本地部署可不填密钥"},
}

func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}
