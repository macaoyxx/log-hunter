// 格式标记槽位：名称数组即槽位事实源（长度 = 槽数，索引 = 颜色类 .mark-N）。
// 扩槽需同步 style.css 的 .mark-0~N 变量
export const SLOT_NAMES = ['红', '黄', '绿', '蓝', '紫']

// AI 全局默认（镜像后端 internal/ai/conf 的 DefaultCtxTokens/DefaultMaxOut——
// 改默认必须两头同步，勿单侧漂移）：前端「空 = 用默认」回落链的终点
export const AI_DEF_CTX = 1048576
export const AI_DEF_MAXOUT = 8192

// 单张贴图字节上限（镜像后端 diag 的 defaultMaxImage = 5<<20，同上两头同步）
export const AI_IMG_MAX_BYTES = 5 << 20
