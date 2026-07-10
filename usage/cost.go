// Package usage — cost.go: the cost adapter over the pricing SSOT (CHG-016).
//
// 设计 (Linus 单一来源): cost = tokens × 单价. 价表不再活在本仓 —— 已迁出到共享
// kit/pricing (github.com/brightman-ai/kit/pricing), 由 deepwork-terminal 与
// deepwork-pro 同源消费, 谁都不拥有它. 本文件只剩一层薄适配: 把 pro 内部历史沿用的
// ComputeCost / HasPrice / CostResult 形态映射到 pricing.Lookup / pricing.Usage,
// 调用方 (session_routes / runtime_session_routes / od_routes / report) 签名不变.
//
// THINKING TOKENS (诚实): Claude transcript 的 usage 无独立 thinking/reasoning 字段 —
// extended thinking 的产出 token 计入 output_tokens. 故 thinking 与 output 同价, 不单列.
//
// 缺价诚实 (RED LINE §5 "不伪造 cost"): pricing.Lookup 未命中 → CostResult.HasPrice=false,
// 调用方显 tokens 不显 cost (绝不蒙一个数).
package usage

import (
	"github.com/brightman-ai/kit/pricing"
)

// CostResult is the computed cost for one (model, token-bundle).
// HasPrice=false ⇒ no matching price row ⇒ caller renders tokens but NOT cost
// (honest: 缺价不蒙). Costs are in Currency units (¥ for CNY, $ for USD).
type CostResult struct {
	InputCost       float64 `json:"input_cost"`
	OutputCost      float64 `json:"output_cost"`
	CacheReadCost   float64 `json:"cache_read_cost"`
	CacheCreateCost float64 `json:"cache_create_cost"`
	TotalCost       float64 `json:"total_cost"`
	Currency        string  `json:"currency"`
	HasPrice        bool    `json:"has_price"`
}

// HasPrice reports whether a model has a known price row (used for honest UI gating).
// Thin delegate to the pricing SSOT.
func HasPrice(model string) bool {
	_, ok := pricing.Lookup(model)
	return ok
}

// ComputeCost is the single per-turn/per-bundle cost adapter (fleet + settings 共用).
// It delegates the price table AND the per-request cost to kit/pricing v0.4 and only
// keeps pro's per-category breakdown + currency + HasPrice shape. thinking tokens
// 计入 output (见包头注), 故无单独 thinking 项. 缺价 → HasPrice=false.
//
// CACHE-WRITE TTL (诚实声明 — 已知微量低估): kit/pricing v0.4 把 cache-write 按 TTL 拆成
// CacheWrite5m (1.25× input) 与 CacheWrite1h (2× input). pro 的逐 turn/逐 bundle 聚合层
// 不携带 5m/1h 拆分 (只有 terminal 的逐消息路径有), 调用方仅传单一 cacheCreate 写入总量.
// 故本适配器把它全记为 CacheWrite5m (保守的 1.25× 基准价). 对包含 1h 缓存写的 turn 这是
// 相对 terminal 逐消息口径的微量低估 —— pro 的 turn 聚合不背 TTL 拆分, 这是已知且可接受的偏差.
//
// TotalCost 以 pricing.Cost (单请求 + 上下文分层权威) 为准; 逐类 breakdown 由 Lookup 的
// base Tier 单价自算 (CostResult 的明细字段, 用于 UI 展示, 与 TotalCost 同源价表).
func ComputeCost(model string, inputTok, outputTok, cacheReadTok, cacheCreateTok int64) CostResult {
	p, ok := pricing.Lookup(model)
	if !ok {
		return CostResult{HasPrice: false}
	}
	// 单一 cacheCreate 写入总量 → CacheWrite5m (保守 1.25× 基准). pro 此层不拆 5m/1h.
	u := pricing.Usage{
		Input:        int(inputTok),
		Output:       int(outputTok),
		CacheRead:    int(cacheReadTok),
		CacheWrite5m: int(cacheCreateTok),
		CacheWrite1h: 0,
	}
	total, currency, _ := pricing.Cost(model, u)
	// 逐类 breakdown (UI 明细): 用 base Tier 单价自算. cacheCreate 计入 5m 价.
	t := p.Tier
	in := float64(inputTok) / 1e6 * t.InputPerM
	out := float64(outputTok) / 1e6 * t.OutputPerM
	cr := float64(cacheReadTok) / 1e6 * t.CacheReadPerM
	cc := float64(cacheCreateTok) / 1e6 * t.CacheWrite5mPerM
	return CostResult{
		InputCost:       round4(in),
		OutputCost:      round4(out),
		CacheReadCost:   round4(cr),
		CacheCreateCost: round4(cc),
		TotalCost:       round4(total),
		Currency:        currency,
		HasPrice:        true,
	}
}

func round4(f float64) float64 { return float64(int64(f*1e4+0.5)) / 1e4 }
