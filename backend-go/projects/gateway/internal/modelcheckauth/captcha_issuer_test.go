package modelcheckauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingCaptchaVerifier struct{}

func (failingCaptchaVerifier) Issue(string) (CaptchaIssueResult, error) {
	return CaptchaIssueResult{}, nil
}

func (failingCaptchaVerifier) Verify(string, string) bool { return true }

func (failingCaptchaVerifier) VerifyWithError(string, string) (bool, error) {
	return false, errors.New("redis down")
}

// DR-P15：J3b 共享验证码驱动的状态存储故障必须 fail-closed 回 500，
// 不得读作“验证码错误”。
func TestHTTPHandlerSharedCaptchaVerifyErrorIsFailClosed(t *testing.T) {
	handler := &HTTPHandler{Captcha: failingCaptchaVerifier{}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"username":"u","password":"p","captchaId":"id","captchaCode":"code"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.login(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
