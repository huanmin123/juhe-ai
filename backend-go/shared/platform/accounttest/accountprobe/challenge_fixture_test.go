package accountprobe

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
)

// challengeQuestionPattern 匹配探针算术挑战报文里的算式部分
//（CreateOutputChallenge 的 outputChallengePromptFormat）。
var challengeQuestionPattern = regexp.MustCompile(`请计算 (\d+)\+(\d+)`)

// challengeAnswerFromBody 从探针请求体解析算术挑战并返回两数之和的十进制
// 文本；请求不含算术挑战（图片/音频等不校验文本的形态）时返回空串。
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
func challengeAnswerFromRequest(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	return challengeAnswerFromBody(body)
}
