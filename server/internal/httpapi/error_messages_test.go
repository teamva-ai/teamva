package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestPublicErrorMessageUsesRequestedLanguageWithoutBilingualCopy(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		message  string
		status   int
		language string
		expected string
	}{
		{
			name: "known error in English", code: "permission_denied", message: "没有权限",
			status:   http.StatusForbidden,
			language: "en-US",
			expected: "Permission denied",
		},
		{
			name: "known error in Chinese", code: "invalid_argument", message: "请求内容不正确",
			status:   http.StatusBadRequest,
			language: "zh-CN",
			expected: "请求内容不正确",
		},
		{
			name: "unknown code uses status", code: "future_conflict", message: "状态已变化",
			status:   http.StatusConflict,
			language: "en-US",
			expected: "The request conflicts with the current resource state",
		},
		{
			name: "existing English remains intact", code: "invalid_argument", message: "invalid request JSON",
			status: http.StatusBadRequest, language: "zh-CN", expected: "The request is invalid",
		},
		{
			name: "empty message gets English", code: "future_error", status: http.StatusServiceUnavailable,
			language: "en-US",
			expected: "This action could not be completed. Try again, then check Activity if it keeps failing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := publicErrorMessage(test.code, test.message, test.status, test.language)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestRespondErrorUsesOneLanguageAndHidesInternalError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("domain error", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		context.Request.Header.Set("Accept-Language", "zh-CN")

		respondError(context, domain.Invalid("请求 JSON 不符合契约"))

		body := decodeErrorBody(t, recorder)
		if body.Error.Message != "请求 JSON 不符合契约" {
			t.Fatalf("unexpected public message: %q", body.Error.Message)
		}
	})

	t.Run("unexpected internal error", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
		context.Request.Header.Set("Accept-Language", "zh-CN")

		respondError(context, errors.New("database password leaked in raw error"))

		body := decodeErrorBody(t, recorder)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", recorder.Code)
		}
		if body.Error.Message != "暂时无法完成这个操作，请重试；如果仍然失败，请到“活动记录”查看详情。" {
			t.Fatalf("unexpected public message: %q", body.Error.Message)
		}
		if strings.Contains(body.Error.Message, "password") {
			t.Fatal("raw internal error leaked to the public response")
		}
	})
}

func decodeErrorBody(t *testing.T, recorder *httptest.ResponseRecorder) struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
} {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body
}
