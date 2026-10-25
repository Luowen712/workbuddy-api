// context_catalog.go context_length / max_output_tokens 字段级静态兜底知识表。
//
// 数据来源四级（model-json-dynamic 任务书；查找链入口在 model_catalog.go 的
// ContextWindowListingV4 / MaxOutputTokensListingV4，本文件是第 2 级）：
//   - 上游动态值（ModelInfo.ContextWindow/MaxTokens，即 maxInputTokens/maxOutputTokens）权威，优先；
//   - 本文件静态知识表（远端零值时补齐；model.json 缺失/损坏时的编译期兜底）；
//   - model.json 本地缓存（数据目录，含运行时 models.dev 按需补值，见 model_catalog.go）；
//   - models.dev 按需拉取（异步不阻塞；仍未知 → context_length 1M 兜底——宁可高估
//     不低估：高估代价是客户端不截断、上游报错可重试；低估代价是下游客户端
//     （Codex/ZCode/Claude Code 按 context_length 提前截断）白白丢上下文；
//     max_output_tokens 未知 → 省略字段（输出上限无合估算据，不编造））。
//
// 知识表**一处定义、CN/global 两域共用**：context_length 是模型固有属性——fork
// 706412584 实测结论「两区是同一套 API 的两次部署」，同 id 上下文一致，无按 realm
// 分表必要（与 effort 档位的 realm 分表刻意不同）。
//
// 值来源（2026-09-24 重校）：本表由现场实测值对齐——对运行中的网关拉取
// GET /v1/models，取上游真实下发的 maxInputTokens / maxOutputTokens 回填。
// 同 id 在 CN 与 global 两域输出上限不同时（实测确认存在，如 glm-5.3 CN 64000 /
// global 48000），共用表**取两者较小值**：输出上限无「宁可高估」的安全侧，
// 高估会让客户端把 max_tokens 抬到上游拒绝的位置（4xx），低估只是少要一点额度。
package upstream

// DefaultContextWindow 知识表也未收录的模型的 context_length 兜底：1M。
// 上游多数大窗口模型的实际量级；高估优于低估（见文件头）。
const DefaultContextWindow int64 = 1000000

// contextCap 一个模型的上下文能力（字段级兜底条目）。
// context 必为正（否则条目无意义，直接走 1M 兜底）；
// maxOutput 为 0 表示输出上限未知 → max_output_tokens 字段省略（不编造）。
type contextCap struct {
	context   int64
	maxOutput int64
}

// contextCapFallback context_length / max_output_tokens 知识表（CN/global 共用）。
// 每条注释标注来源：实测 = 2026-09-24 从运行网关 GET /v1/models 取上游真实下发值
// （同 id 两域不一致时取较小值）；估算 = 同族外推。
var contextCapFallback = map[string]contextCap{
	// ---- GLM 家族（z-ai）----
	"glm-5.2":       {context: 1000000, maxOutput: 48000}, // 实测（CN 64000 / global 48000，取小）
	"glm-5.1":       {context: 200000, maxOutput: 48000},  // 实测（CN 48000）
	"glm-5.3":       {context: 1000000, maxOutput: 48000}, // 实测（CN 64000 / global 48000，取小）
	"glm-5.3-flash": {context: 1000000, maxOutput: 32000}, // 实测（CN 131072 / global 32000，取小）
	"glm-5v-turbo":  {context: 200000, maxOutput: 64000},  // 实测（CN 64000）

	// ---- Kimi 家族（moonshot）----
	"kimi-k2.7":         {context: 256000, maxOutput: 32000},  // 实测（CN 32000）
	"kimi-k2.6":         {context: 256000, maxOutput: 32000},  // 实测（CN/global 均 32000）
	"kimi-k2.5":         {context: 256000, maxOutput: 32000},  // 实测（CN 256K/32000）
	"kimi-k3":           {context: 1000000, maxOutput: 32000}, // 实测（global 32000）
	"kimi-k2.8-preview": {context: 1000000, maxOutput: 32000}, // 实测（CN 64000 / global 32000，取小）

	// ---- MiniMax / 混元（tencent）----
	"minimax-m3":    {context: 512000, maxOutput: 64000},  // 实测（CN 512K/64000）
	"hy3":           {context: 192000, maxOutput: 64000},  // 实测（CN/global 均 192K/64000）
	"hy3-preview":   {context: 262144, maxOutput: 64000},  // 估算（网关未见该 id，沿用同族）
	"hy4-preview":   {context: 1000000, maxOutput: 64000}, // 实测（CN/global 均 1M/64000）
	"hy4-preview-x": {context: 1000000, maxOutput: 64000}, // 估算（网关未见该 id，同族 hy4-preview）

	// ---- DeepSeek 家族 ----
	"deepseek-v4-pro":     {context: 1000000, maxOutput: 128000}, // 实测（CN 1M/128000）
	"deepseek-v4-flash":   {context: 1000000, maxOutput: 50000},  // 实测（CN 1M/50000）
	"deepseek-v4.1-flash": {context: 1000000, maxOutput: 128000}, // 实测（CN/global 均 1M/128000）

	// ---- OpenAI / Google（global 域家族）----
	"gpt-6-astra":      {context: 1000000, maxOutput: 128000}, // 实测（global 1000000/128000）
	"gpt-5.6-sol":      {context: 1000000, maxOutput: 128000}, // 实测（global）
	"gpt-5.6-terra":    {context: 1000000, maxOutput: 128000}, // 实测（global）
	"gpt-5.6-luna":     {context: 1000000, maxOutput: 128000}, // 实测（global）
	"gpt-5.5":          {context: 1000000, maxOutput: 128000}, // 实测（global）
	"gpt-5.4":          {context: 272000, maxOutput: 72000},   // 实测（global 272000/72000）
	"gpt-5.3-codex":    {context: 272000, maxOutput: 72000},   // 实测（global 272000/72000）
	"gemini-3.5-flash": {context: 1000000, maxOutput: 65536},  // 实测（global 1000000/65536）

	// ---- global 域路由别名/别名模型 ----
	"auto": {context: 256000, maxOutput: 32000}, // 实测（CN 256000/32000）
}

// ContextWindowListing 模型在 /v1/models 的 context_length（三级查找）：
// remote（上游 maxInputTokens）>0 时权威；否则查知识表；仍未收录 → DefaultContextWindow
// （1M，宁可高估不低估）。绝不再透出假 131072。
func ContextWindowListing(model string, remote int64) int64 {
	if remote > 0 {
		return remote
	}
	if cap, ok := contextCapFallback[model]; ok && cap.context > 0 {
		return cap.context
	}
	return DefaultContextWindow
}

// MaxOutputTokensListing 模型在 /v1/models 的 max_output_tokens（三级查找）：
// remote（上游 maxOutputTokens）>0 时权威；否则查知识表；仍未收录 → ok=false
// （调用方省略字段，不编造输出上限）。与 ContextWindowListing 的 1M 兜底刻意不同：
// 输出上限无「宁可高估」的安全侧，未知即省略。
func MaxOutputTokensListing(model string, remote int64) (int64, bool) {
	if remote > 0 {
		return remote, true
	}
	if cap, ok := contextCapFallback[model]; ok && cap.maxOutput > 0 {
		return cap.maxOutput, true
	}
	return 0, false
}