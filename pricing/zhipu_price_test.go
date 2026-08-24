package pricing

import (
	"encoding/json"
	"strings"
	"testing"
)

// 智谱 GLM 价卡 —— 读自厂商自己的价格页（open.bigmodel.cn/pricing，2026-08-22 渲染后读表）。
//
// 这批用例守两件事：一件是钱本身钉到分，另一件是两个**会静默算错**的结构陷阱。

// GLM-4.7 是这张表里唯一按两个维度分档的模型，压成一档会差 2 倍 —— 那是凭空造钱，不是取整。
//
//	输入<32k 且 输出<200   →  ¥2  / ¥8  / ¥0.4
//	输入<32k 且 输出≥200   →  ¥3  / ¥14 / ¥0.6
//	输入≥32k              →  ¥4  / ¥16 / ¥0.8
func TestGLM47_PricesOnBothLengthAxes(t *testing.T) {
	at := mustDate("2026-08-22")
	quote, ok := DefaultCatalog().Quote(RequestQuery{Model: "glm-4.7", At: at, ServiceTier: "standard"})
	if !ok {
		t.Fatal("glm-4.7 无价 —— 那正是本轮要修的 price_rule_missing")
	}

	cases := []struct {
		name string
		u    Usage
		want float64
	}{
		{
			// 短上下文 + 短回答：基础档。（写这条时我第一版给了 1M 上下文，被这个用例当场抓住 ——
			// 1M 早就跨过 32k 了，那是高档。）
			name: "短上下文短回答走基础档",
			u:    Usage{Input: 10_000, Output: 100},
			want: 10_000*2/1e6 + 100*8/1e6,
		},
		{
			// 输出跨过 200 → 中档。同样的 token 量，价格从 ¥10 变 ¥17，这就是压档的代价。
			name: "短上下文长回答走输出档",
			u:    Usage{Input: 31_000, Output: 1_000_000},
			want: 31_000*3/1e6 + 14,
		},
		{
			// 上下文跨过 32k → 高档，且**无视回答长度**：厂商长上下文那一行不带输出条件。
			name: "长上下文走上下文档，回答再短也一样",
			u:    Usage{Input: 1_000_000, Output: 10},
			want: 4 + 10*16/1e6,
		},
		{
			// 两个条件同时成立时，上下文档必须赢 —— 否则长上下文的长回答会按中档少收 ¥1/¥2。
			name: "两档同时成立时上下文档优先",
			u:    Usage{Input: 1_000_000, Output: 1_000_000},
			want: 4 + 16,
		},
		{
			// 缓存读也跟着档位走：它算进上下文长度，也按档位计价。
			name: "缓存读计入上下文长度并按该档计价",
			u:    Usage{Input: 1_000, CacheRead: 1_000_000, Output: 10},
			want: 1_000*4/1e6 + 1_000_000*0.8/1e6 + 10*16/1e6,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, currency := quote.Cost(tc.u)
			if currency != "CNY" {
				t.Fatalf("currency = %q, want CNY —— 智谱按元报价", currency)
			}
			if !nearly(got, tc.want) {
				t.Fatalf("cost = %v, want %v", got, tc.want)
			}
		})
	}
}

// 厂商区间是左闭的（「[32,200)」从 32k 起算），而引擎比较是严格大于 —— 差一个 token。
// 写成 31_999 / 199 就是为了把这个差补上；这个用例是那行注释的可执行版本。
func TestGLM47_BandBoundariesMatchTheVendorsHalfOpenRanges(t *testing.T) {
	at := mustDate("2026-08-22")
	quote, _ := DefaultCatalog().Quote(RequestQuery{Model: "glm-4.7", At: at, ServiceTier: "standard"})

	// 恰好 32,000 tokens 上下文 → 已进高档（厂商写的是 [32,200)）。
	atBoundary, _ := quote.Cost(Usage{Input: 32_000, Output: 1})
	justBelow, _ := quote.Cost(Usage{Input: 31_999, Output: 1})
	if !nearly(atBoundary, 32_000*4/1e6+16/1e6) {
		t.Errorf("32k 整应落高档，got %v", atBoundary)
	}
	if !nearly(justBelow, 31_999*2/1e6+8/1e6) {
		t.Errorf("31,999 应仍在基础档，got %v", justBelow)
	}

	// 恰好 200 tokens 输出 → 已进中档（厂商写的是 [0.2+)）。
	out200, _ := quote.Cost(Usage{Input: 1_000, Output: 200})
	out199, _ := quote.Cost(Usage{Input: 1_000, Output: 199})
	if !nearly(out200, 1_000*3/1e6+200*14/1e6) {
		t.Errorf("200 tokens 输出应进中档，got %v", out200)
	}
	if !nearly(out199, 1_000*2/1e6+199*8/1e6) {
		t.Errorf("199 tokens 输出应仍在基础档，got %v", out199)
	}
}

// THE 前缀陷阱，而且不止一处。matchesAnyModel 是 exact-or-prefix-plus-dash：
//
//	"glm-4.7" 也匹配 "glm-4.7-flashx"  —— ¥0.5 vs ¥2–4，最高算高 8 倍
//	"glm-5"   也匹配 "glm-5-turbo"     —— ¥4–6 vs ¥5–7
//
// Quote 是先匹配先赢，所以具体型号必须排在会吞掉它的家族规则之前。
// 这条用例失败通常不是价格写错了，而是**有人调整了规则顺序**。
func TestGLM_SpecificIdsBeatTheirPrefixes(t *testing.T) {
	at := mustDate("2026-08-22")
	for _, tc := range []struct {
		model              string
		in, out, cacheRead float64
		banded             bool
	}{
		{model: "glm-4.7-flashx", in: 0.5, out: 3, cacheRead: 0.1},
		{model: "glm-4.7-flash", in: 0, out: 0, cacheRead: 0}, // 厂商标「免费」——零是真实的价，不是"没查到"
		{model: "glm-5-turbo", in: 5, out: 22, cacheRead: 1.2, banded: true},
	} {
		quote, ok := DefaultCatalog().Quote(RequestQuery{Model: tc.model, At: at, ServiceTier: "standard"})
		if !ok {
			t.Fatalf("%s 无价 —— 它会掉进上一级家族规则按错价计费", tc.model)
		}
		if quote.Price.InputPerM != tc.in || quote.Price.OutputPerM != tc.out || quote.Price.CacheReadPerM != tc.cacheRead {
			t.Fatalf("%s = %v/%v/%v, want %v/%v/%v（八成是规则顺序被动过）",
				tc.model, quote.Price.InputPerM, quote.Price.OutputPerM, quote.Price.CacheReadPerM,
				tc.in, tc.out, tc.cacheRead)
		}
		if banded := quote.Price.Above != nil; banded != tc.banded {
			t.Fatalf("%s 分档状态 = %v, want %v（粘错了别人的档位表）", tc.model, banded, tc.banded)
		}
		// 输出档只属于 4.7 / 4.5-Air，别的模型不该有。
		if quote.Price.AboveOutput != nil {
			t.Fatalf("%s 不该有输出档，got %+v", tc.model, quote.Price)
		}
	}
}

// glm-5.1 曾经以【平价 ¥6/¥24】待在内嵌价表里 —— 那只是它的短档，超过 32k 的请求被少算三分之一。
// 搬进 catalog 就是为了能表达档位；这条守着它别再退回平价。
func TestGLM51_HasTheLongContextBandItUsedToLose(t *testing.T) {
	quote, ok := DefaultCatalog().Quote(RequestQuery{
		Model: "glm-5.1", At: mustDate("2026-08-22"), ServiceTier: "standard"})
	if !ok {
		t.Fatal("glm-5.1 无价（它从内嵌表搬到了 catalog）")
	}
	short, _ := quote.Cost(Usage{Input: 10_000, Output: 1_000_000})
	long, _ := quote.Cost(Usage{Input: 1_000_000, Output: 1_000_000})
	if !nearly(short, 10_000*6/1e6+24) {
		t.Errorf("短档 = %v, want ¥6/¥24", short)
	}
	if !nearly(long, 8+28) {
		t.Errorf("长档 = %v, want ¥8/¥28 —— 平价会少算三分之一", long)
	}
}

// GLM-5.3 / 5.2 在整个 1M 上下文里是平价 —— 没有任何长度档。
func TestGLM5x_IsFlatAcrossItsWholeContext(t *testing.T) {
	at := mustDate("2026-08-22")
	for _, model := range []string{"glm-5.3", "glm-5.2"} {
		quote, ok := DefaultCatalog().Quote(RequestQuery{Model: model, At: at, ServiceTier: "standard"})
		if !ok {
			t.Fatalf("%s 无价", model)
		}
		if quote.Price.Above != nil || quote.Price.AboveOutput != nil {
			t.Fatalf("%s 不该分档：厂商价表整行只有一个价", model)
		}
		if quote.Price.InputPerM != 8 || quote.Price.OutputPerM != 28 || quote.Price.CacheReadPerM != 2 {
			t.Fatalf("%s = %v/%v/%v, want 8/28/2 CNY", model,
				quote.Price.InputPerM, quote.Price.OutputPerM, quote.Price.CacheReadPerM)
		}
		// 小请求和巨型请求必须按同一个单价走。
		small, _ := quote.Cost(Usage{Input: 1_000_000, Output: 1_000_000})
		huge, _ := quote.Cost(Usage{Input: 900_000_000, Output: 1_000_000})
		if !nearly(small, 8+28) || !nearly(huge, 900*8+28) {
			t.Fatalf("%s 平价被破坏：small=%v huge=%v", model, small, huge)
		}
	}
}

// 缓存存储当前是「限时免费」。零必须是**写进去的零**，不是漏了字段——
// 促销结束时应新增一条带生效日期的规则，而不是回来改这条。
func TestGLM_CacheWriteIsAnExplicitZeroWhileThePromotionLasts(t *testing.T) {
	quote, _ := DefaultCatalog().Quote(RequestQuery{
		Model: "glm-5.3", At: mustDate("2026-08-22"), ServiceTier: "standard"})
	got, _ := quote.Cost(Usage{CacheWrite5m: 10_000_000})
	if got != 0 {
		t.Fatalf("缓存写 = %v, 厂商现在标「限时免费」", got)
	}
}

func nearly(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// 单价卡这一行存在的唯一理由是【让总额可核对】。对分档模型只印最便宜那一档，等于把它反过来用：
// 读者拿 ¥2/M 怎么算都对不上按 ¥4/M 收的钱，而且看不出为什么。
//
// 实测（加这条之前）：gpt-5.6-sol 对外写 $5/$30，而编码会话里占绝大多数的长上下文请求实收
// $10/$45 —— 差 2 倍，且发生在用户金额最大的那一行上。
func TestPublishedRates_BandedModelsPublishEveryBand(t *testing.T) {
	// 分档模型：每一档都要出现，且必须能对上 Cost() 真正会收的价。
	for _, model := range []string{"glm-4.7", "glm-5.1", "glm-4.5-air", "gpt-5.6-sol"} {
		cards := PublishedRates(model)
		if len(cards) < 2 {
			t.Fatalf("%s 是分档模型，却只发布了 %d 张卡 —— 总额将无法被核对", model, len(cards))
		}
		var long *RateCard
		for i := range cards {
			if cards[i].Band == BandLongContext {
				long = &cards[i]
			}
		}
		if long == nil {
			t.Fatalf("%s 缺长上下文档", model)
		}
		if long.Threshold <= 0 {
			t.Errorf("%s 长档没写边界，读者无从判断自己落在哪一档", model)
		}
		// 卡上的价必须就是引擎真收的价：拿一个远超边界的请求去反推每 1M 的实收单价。
		quote, ok := DefaultCatalog().Quote(RequestQuery{Model: model, At: mustDate("2026-08-22"), ServiceTier: "standard"})
		if !ok {
			t.Fatalf("%s 无价", model)
		}
		charged, _ := quote.Cost(Usage{Input: 10_000_000})
		if !nearly(charged/10, long.InputPerM) {
			t.Errorf("%s 长档卡写 %v/M，实收 %v/M —— 卡和钱对不上", model, long.InputPerM, charged/10)
		}
	}

	// 平价模型不受影响：仍然只有一张卡，且不带档位标记。
	for _, model := range []string{"glm-5.3", "glm-5.2"} {
		cards := PublishedRates(model)
		if len(cards) != 1 || cards[0].Band != "" {
			t.Fatalf("%s 是平价，不该长出档位，got %+v", model, cards)
		}
	}

	// Kimi 的两套卡是【两个平台的标价】，不是档位 —— 这两件事不能混为一谈：
	// 一个是"同一份钱的不同报价单"，另一个是"同一份报价单里的不同区间"。
	kimi := PublishedRates("k3")
	if len(kimi) != 2 {
		t.Fatalf("k3 应有两套平台价，got %d", len(kimi))
	}
	for _, c := range kimi {
		if c.Band != "" {
			t.Errorf("k3 不分档，却带了档位标记 %q", c.Band)
		}
	}
	if kimi[0].Primary == kimi[1].Primary {
		t.Error("两套平台价里必须恰有一套是本行算钱用的")
	}
}

// `primary:false` 是「这是另一个平台的报价单，不是本表的另一档」这句话本身 —— 被 omitempty 吞掉，
// 消费方只能读到 undefined，两件事就分不开了。实测就是这么错的：Kimi 的两套【平台价】被渲染成
// 「¥20/¥100 · $3/$15」，看起来像同一张表的两个档位。
//
// 与 Credits.WholeDays 同一族：凡「false 才是信息」的布尔，必须无条件上 wire。
func TestRateCard_PrimaryFalseSurvivesTheWire(t *testing.T) {
	blob, err := json.Marshal(RateCard{Currency: "USD", InputPerM: 3, OutputPerM: 15, Primary: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"primary":false`) {
		t.Fatalf("primary=false 被 omitempty 吞了，档位与平台价将无法区分：%s", blob)
	}
}
