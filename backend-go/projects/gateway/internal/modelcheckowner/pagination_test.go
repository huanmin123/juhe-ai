package modelcheckowner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type paginationQualityManager struct {
	fakeQualityManager
	page     int
	pageSize int
}

func (m *paginationQualityManager) ListSchedules(_ context.Context, _ string, page, pageSize int) (QualityScheduleList, error) {
	m.page = page
	m.pageSize = pageSize
	return QualityScheduleList{}, nil
}

func TestParsePageUsesNodeCompatibleDefaultsAndBounds(t *testing.T) {
	page, pageSize, err := parsePage(httptest.NewRequest(http.MethodGet, "/runs", nil))
	if err != nil {
		t.Fatal(err)
	}
	if page != 1 || pageSize != 20 {
		t.Fatalf("default pagination=%d,%d, want page=1 pageSize=20", page, pageSize)
	}

	for _, test := range []struct {
		raw      string
		expected int
	}{{"1", 1}, {"100", 100}} {
		_, parsedSize, err := parsePage(httptest.NewRequest(http.MethodGet, "/runs?pageSize="+test.raw, nil))
		if err != nil {
			t.Fatalf("pageSize=%s: %v", test.raw, err)
		}
		if parsedSize != test.expected {
			t.Fatalf("pageSize=%s parsed as %d", test.raw, parsedSize)
		}
	}

	for _, raw := range []string{"0", "101", "not-a-number"} {
		_, _, err := parsePage(httptest.NewRequest(http.MethodGet, "/runs?pageSize="+raw, nil))
		if err == nil || !strings.Contains(err.Error(), "1 到 100") {
			t.Fatalf("pageSize=%s err=%v, want visible 1..100 validation error", raw, err)
		}
	}
}

func TestRunAndQualityScheduleRoutesSharePaginationContract(t *testing.T) {
	handler := newTestHTTPHandler()
	runService := &scopedRunService{}
	handler.Service = runService

	runs := httptest.NewRecorder()
	handler.ServeHTTP(runs, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if runs.Code != http.StatusOK || runService.query.Page != 1 || runService.query.PageSize != 20 {
		t.Fatalf("runs status=%d query=%+v, want page=1 pageSize=20", runs.Code, runService.query)
	}

	quality := &paginationQualityManager{}
	handler.Quality = quality
	schedules := httptest.NewRecorder()
	handler.ServeHTTP(schedules, httptest.NewRequest(http.MethodGet, "/quality-schedules", nil))
	if schedules.Code != http.StatusOK || quality.page != 1 || quality.pageSize != 20 {
		t.Fatalf("quality schedules status=%d pagination=%d,%d, want page=1 pageSize=20", schedules.Code, quality.page, quality.pageSize)
	}

	for _, path := range []string{"/runs", "/quality-schedules"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?pageSize=101", nil))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "1 到 100") {
			t.Fatalf("%s over-bound status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
