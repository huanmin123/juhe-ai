// 系统内置题库题的种子数据（模型检测设计 §5.7 系统内置题，2026-10-09 起）：
// 一批社区流行的经典模型能力/降智检测题（草莓字母计数、9.11 与 9.9 比较、
// 糖果多步算术、牛奶稀释常识、惯性陷阱、百分比陷阱、字符串倒序等），随
// maintenance --seed 以固定 id 幂等写入 model_check_question_bank
// （status='approved'、created_by='system'、is_builtin=1），供用户直接选用
// 或作为自拟题参考。应用层对内置题禁止编辑/删除/审核，题面调整随版本演进
// 本文件。
package schema

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// builtinQuestionKeyPointsJSON 序列化评分要点为 key_points_json 列文本
//（JSON 数组，与 gateway store 的 keyPointsJSONValue 读写契约一致）。
func builtinQuestionKeyPointsJSON(question BuiltinModelCheckQuestion) (string, error) {
	encoded, err := json.Marshal(question.KeyPoints)
	if err != nil {
		return "", fmt.Errorf("marshal builtin question %s key points: %w", question.ID, err)
	}
	return string(encoded), nil
}

// builtinQuestionTitleNorm 复刻 gateway modelcheckquestionbank.NormalizeText
// （NFKC 后去空白/标点/符号并小写）；内置题标题只含 CJK 与 ASCII，NFKC 为
// 恒等变换，故只做去符号与小写。查重按 title_norm 精确匹配，种子行必须写
// 归一化值，否则用户提交同标题时无法命中 409。两侧归一化规则必须同步改动。
func builtinQuestionTitleNorm(title string) string {
	var builder strings.Builder
	for _, item := range strings.TrimSpace(title) {
		if unicode.IsSpace(item) || unicode.IsPunct(item) || unicode.IsSymbol(item) {
			continue
		}
		builder.WriteRune(unicode.ToLower(item))
	}
	return builder.String()
}

// BuiltinModelCheckQuestion 是一道系统内置题的种子定义。ID 是稳定主键：
// 重复 seed 走 INSERT OR IGNORE，不覆盖用户可能已产生的引用；改题面等于
// 发新版本数据，不得改动已有 ID 对应的题干语义。
type BuiltinModelCheckQuestion struct {
	ID              string
	Title           string
	QuestionText    string
	ReferenceAnswer string
	KeyPoints       []string
}

// builtinModelCheckQuestionCreator 是内置题的 created_by / created_scope
// 统一占位：不是真实系统账户 id，仅标识来源为系统种子。
const builtinModelCheckQuestionCreator = "system"

var builtinModelCheckQuestions = []BuiltinModelCheckQuestion{
	{
		ID:              "mcq-builtin-strawberry-r",
		Title:           "内置·strawberry 有几个 r（草莓题）",
		QuestionText:    "英文单词 \"strawberry\" 一共有几个字母 r？请先给出数量，再逐个字母拼出单词并标出每个 r 的位置。",
		ReferenceAnswer: "3 个。strawberry = s·t·r·a·w·b·e·r·r·y，第 3、8、9 个字母是 r，共 3 个 r。",
		KeyPoints:       []string{"结论为 3 个 r", "逐字母完整拼写 strawberry", "正确指出 r 出现在第 3、8、9 位"},
	},
	{
		ID:              "mcq-builtin-9-11-vs-9-9",
		Title:           "内置·9.11 和 9.9 哪个更大",
		QuestionText:    "9.11 和 9.9 哪个数更大？请给出结论并解释理由。",
		ReferenceAnswer: "9.9 更大。9.9 = 9.90，逐位比较小数部分 0.90 > 0.11，所以 9.9 > 9.11；不能被版本号阅读习惯（9.11 版本晚于 9.9）误导。",
		KeyPoints:       []string{"结论为 9.9 更大", "按小数逐位比较解释（9.90 与 9.11）"},
	},
	{
		ID:              "mcq-builtin-hanzi-count",
		Title:           "内置·「人工智能改变世界」有几个汉字",
		QuestionText:    "「人工智能改变世界」这句话一共有几个汉字？请直接给出数量。",
		ReferenceAnswer: "8 个：人、工、智、能、改、变、世、界。",
		KeyPoints:       []string{"答案为 8 个", "能完整列出这 8 个汉字"},
	},
	{
		ID:              "mcq-builtin-candy-steps",
		Title:           "内置·桌上糖果还剩几颗（糖果题）",
		QuestionText:    "桌上有 7 颗糖果，小红拿走 3 颗，妈妈又放回 2 颗，接着小明拿走剩下糖果的一半。现在桌上还剩几颗糖果？请给出完整计算过程。",
		ReferenceAnswer: "3 颗。计算过程：7 − 3 = 4，4 + 2 = 6，6 ÷ 2 = 3，桌上还剩 3 颗糖果。",
		KeyPoints:       []string{"最终答案为 3 颗", "三步计算正确（7-3=4、4+2=6、6÷2=3）"},
	},
	{
		ID:              "mcq-builtin-milk-dilution",
		Title:           "内置·牛奶兑水后还剩多少纯牛奶（牛奶题）",
		QuestionText:    "一杯纯牛奶 250 毫升，先喝掉一半，再用水加满，摇匀后又喝掉一半。此时杯中还有多少毫升纯牛奶？请给出计算过程。",
		ReferenceAnswer: "62.5 毫升纯牛奶。第一次喝掉一半后剩 125 毫升纯牛奶；加水加满只改变总体积（回到 250 毫升），纯牛奶仍为 125 毫升；再喝掉一半，杯中液体总量为 125 毫升，其中纯牛奶 125 ÷ 2 = 62.5 毫升。注意区分「液体总量」与「纯牛奶量」。",
		KeyPoints:       []string{"纯牛奶为 62.5 毫升", "正确理解加水不改变纯牛奶量、再喝一半按当前浓度减半"},
	},
	{
		ID:              "mcq-builtin-third-son-trap",
		Title:           "内置·小明的爸爸三个儿子叫什么（惯性陷阱）",
		QuestionText:    "小明的爸爸有三个儿子，大儿子叫大毛，二儿子叫二毛，请问三儿子叫什么？",
		ReferenceAnswer: "小明。题目开头「小明的爸爸」已经说明小明本人是儿子之一，大毛、二毛是另外两个儿子，三儿子就是小明自己。",
		KeyPoints:       []string{"回答「小明」", "不被大毛、二毛的命名惯性带偏（不答「三毛」）"},
	},
	{
		ID:              "mcq-builtin-price-percent",
		Title:           "内置·先涨价 10% 再降价 10% 是多少",
		QuestionText:    "一件商品标价 100 元，先涨价 10%，再降价 10%，现在价格是多少元？请给出计算过程。",
		ReferenceAnswer: "99 元。涨价后 100 × 1.1 = 110 元，再降价 110 × 0.9 = 99 元。先涨后降相同百分比不会回到原价（基数变了）。",
		KeyPoints:       []string{"最终价格为 99 元", "两步计算正确（110 元、99 元）", "点出先涨后降不对称、不会回到原价"},
	},
	{
		ID:              "mcq-builtin-string-reverse",
		Title:           "内置·model-check 逐字符倒序",
		QuestionText:    "请把字符串 \"model-check\" 逐字符倒序输出，只输出倒序后的字符串，不要添加任何解释。",
		ReferenceAnswer: "kcehc-ledom",
		KeyPoints:       []string{"输出精确等于 kcehc-ledom", "无多余字符、空格或解释"},
	},
}
