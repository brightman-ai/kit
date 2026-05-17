// Package log provides error code definitions for structured logging.
package log

// Error code ranges:
// 1000-1999: config
// 2000-2999: storage
// 3000-3999: memory
// 4000-4999: agent
// 5000-5999: llm
// 6000-6999: browser
// 7000-7999: skill
// 8000-8999: webui
// 9000-9999: desktop

// Config errors (1000-1999)
const (
	ECConfigNotFound    = 1001 // 配置文件未找到
	ECConfigParseError  = 1002 // 配置解析失败
	ECConfigValidation  = 1003 // 配置验证失败
	ECConfigEnvMissing  = 1004 // 环境变量缺失
)

// Storage errors (2000-2999)
const (
	ECDBOpenFailed    = 2001 // 数据库打开失败
	ECDBMigrateFailed = 2002 // 数据库迁移失败
	ECDBQueryFailed   = 2003 // 查询执行失败
	ECDBTxFailed      = 2004 // 事务失败
)

// Memory errors (3000-3999)
const (
	ECMemSessionNotFound  = 3001 // 会话不存在
	ECMemVectorFailed     = 3002 // 向量化失败
	ECMemRAGSearchFailed  = 3003 // RAG 搜索失败
	ECMemStorageFull      = 3004 // 内存存储已满
)

// Agent errors (4000-4999)
const (
	ECAgentIntentFailed  = 4001 // 意图解析失败
	ECAgentPlanFailed    = 4002 // 计划生成失败
	ECAgentExecuteFailed = 4003 // 执行步骤失败
	ECAgentReviewFailed  = 4004 // 审查失败
	ECAgentLoopDetected  = 4005 // 检测到循环
	ECAgentTimeout       = 4006 // Agent 执行超时
)

// LLM errors (5000-5999)
const (
	ECLLMRequestFailed     = 5001 // LLM 请求失败
	ECLLMRateLimited       = 5002 // 触发速率限制
	ECLLMTokenExceeded     = 5003 // Token 超限
	ECLLMInvalidResponse   = 5004 // 响应格式无效
	ECLLMStreamInterrupted = 5005 // 流式响应中断
	ECLLMAPIKeyInvalid     = 5006 // API Key 无效
)

// Browser errors (6000-6999)
const (
	ECBrowserLaunchFailed   = 6001 // 浏览器启动失败
	ECBrowserNavigateFailed = 6002 // 导航失败
	ECBrowserElementNotFound = 6003 // 元素未找到
	ECBrowserClickFailed    = 6004 // 点击失败
	ECBrowserInputFailed    = 6005 // 输入失败
	ECBrowserTimeout        = 6006 // 操作超时
	ECBrowserCDPError       = 6007 // CDP 协议错误
	ECBrowserOccluded       = 6008 // 元素被遮挡
)

// Skill errors (7000-7999)
const (
	ECSkillNotFound      = 7001 // Skill 未找到
	ECSkillExecuteFailed = 7002 // Skill 执行失败
	ECSkillInvalidArgs   = 7003 // 参数无效
)

// WebUI errors (8000-8999)
const (
	ECWebUIBindFailed    = 8001 // 端口绑定失败
	ECWebUIHandlerError  = 8002 // 处理器错误
)

// Desktop errors (9000-9999)
const (
	ECDesktopWindowFailed  = 9001 // 窗口创建失败
	ECDesktopBindingError  = 9002 // 绑定错误
	ECDesktopFrontendError = 9003 // 前端错误
)
