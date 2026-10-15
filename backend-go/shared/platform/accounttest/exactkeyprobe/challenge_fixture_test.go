package exactkeyprobe

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// testChallenge 是直接调用校验函数的测试用固定挑战：与生产报文同格式，
// 期望答案 46。
var testChallenge = probeChallenge{
	Prompt:   "接口连通性自动测试：请计算 12+34，只回复数字",
	Expected: "46",
}

// challengeQuestionPattern 匹配探针算术挑战报文里的算式部分。
var challengeQuestionPattern = regexp.MustCompile(`请计算 (\d+)\+(\d+)`)

// challengeAnswerFromBody 从探针请求体解析算术挑战并返回两数之和的十进制
// 文本；请求不含算术挑战（图片形态）时返回空串。
func challengeAnswerFromBody(body []byte) string {
	match := challengeQuestionPattern.FindSubmatch(body)
	if match == nil {
		return ""
	}
	left, _ := strconv.Atoi(string(match[1]))
	right, _ := strconv.Atoi(string(match[2]))
	return strconv.Itoa(left + right)
}

// challengeAnswerFromRequest 读取请求体后解析算术挑战答案，供假上游回显。
func challengeAnswerFromRequest(request *http.Request) string {
	body, _ := io.ReadAll(request.Body)
	return challengeAnswerFromBody(body)
}

// echoAnswer 把响应模板里的占位答案换成请求体里的真实算术答案。
func echoAnswer(raw []byte, template string) string {
	return strings.ReplaceAll(template, `"__ANSWER__"`, `"`+challengeAnswerFromBody(raw)+`"`)
}

// challengeAnswerFromDecoded 从已解码的探针请求体里找算术题并返回答案。
// handler 已用 json.Decoder 消费 body 时用它，避免再读请求体。
func challengeAnswerFromDecoded(body map[string]any) string {
	raw, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	return challengeAnswerFromBody(raw)
}
