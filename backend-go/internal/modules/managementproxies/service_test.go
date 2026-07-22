package managementproxies

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListNormalizesPaginationAndMapsRows(t *testing.T) {
	lastTestedAt := time.Date(2026, 7, 10, 8, 30, 0, 0, time.UTC)
	description := "说明"
	username := "proxy-user"
	latencyMs := 123
	outboundIP := "203.0.113.8"
	outboundRegion := "US"
	lastTestMessage := "ok"
	store := &proxyOptionStoreStub{
		listResult: port.ManagementProxyListResult{
			Items: []port.ManagementProxySummary{
				{
					ID:              "proxy_a",
					Name:            "代理 A",
					Description:     &description,
					Type:            "socks5h",
					Host:            "proxy.example.com",
					Port:            1080,
					Username:        &username,
					Enabled:         true,
					TestStatus:      "passed",
					LatencyMs:       &latencyMs,
					OutboundIP:      &outboundIP,
					OutboundRegion:  &outboundRegion,
					LastTestMessage: &lastTestMessage,
					LastTestedAt:    &lastTestedAt,
				},
			},
			HasMore: true,
		},
	}
	service := NewService(store)

	got, err := service.List(context.Background(), ListInput{Keyword: "  代理  ", Page: 2, PageSize: 500})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Keyword != "代理" || store.listInput.Limit != maxListPageSize+1 || store.listInput.Offset != maxListPageSize {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if got.Page != 2 || got.PageSize != maxListPageSize || got.Total != 202 || !got.HasMore {
		t.Fatalf("pagination = %+v", got)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
	item := got.Items[0]
	if item.ID != "proxy_a" ||
		item.Description == nil ||
		*item.Description != "说明" ||
		item.Username == nil ||
		*item.Username != "proxy-user" ||
		item.LatencyMs == nil ||
		*item.LatencyMs != 123 ||
		item.LastTestedAt == nil ||
		!item.LastTestedAt.Equal(lastTestedAt) {
		t.Fatalf("item = %+v", item)
	}
}

func TestListDefaultsPaginationAndDetectsExtraRow(t *testing.T) {
	store := &proxyOptionStoreStub{
		listResult: port.ManagementProxyListResult{
			Items: []port.ManagementProxySummary{
				{ID: "proxy_1", Name: "代理 1"},
				{ID: "proxy_2", Name: "代理 2"},
			},
		},
	}
	service := NewService(store)

	got, err := service.List(context.Background(), ListInput{Page: -10, PageSize: 1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Limit != 2 || store.listInput.Offset != 0 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if got.Page != 1 || got.PageSize != 1 || got.Total != 2 || !got.HasMore || len(got.Items) != 1 {
		t.Fatalf("result = %+v", got)
	}
}

func TestListClampsDeepPageToWindow(t *testing.T) {
	store := &proxyOptionStoreStub{}
	service := NewService(store)

	got, err := service.List(context.Background(), ListInput{Page: 999, PageSize: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Limit != 21 || store.listInput.Offset != 980 {
		t.Fatalf("store list input = %+v, want page clamped to 1001 row window", store.listInput)
	}
	if got.Page != 50 || got.PageSize != 20 || got.Total != 980 || got.HasMore {
		t.Fatalf("result = %+v", got)
	}
}

func TestListReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&proxyOptionStoreStub{listErr: want})

	_, err := service.List(context.Background(), ListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("List() error = %v, want %v", err, want)
	}
}

func TestOptionsTrimsKeywordAndClampsLimit(t *testing.T) {
	store := &proxyOptionStoreStub{
		options: []port.ManagementProxyOption{{
			ID:      "proxy_a",
			Name:    "代理 A",
			Type:    "http",
			Enabled: true,
		}},
	}
	service := NewService(store)

	got, err := service.Options(context.Background(), OptionListInput{
		Keyword:     "  代理  ",
		Limit:       500,
		SelectedIDs: []string{" proxy_c ", "proxy_a", "proxy_c", "proxy_b"},
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Keyword != "代理" || store.input.Limit != 50 {
		t.Fatalf("store input = %+v, want trimmed keyword and limit 50", store.input)
	}
	if gotIDs, wantIDs := store.input.SelectedIDs, []string{"proxy_a", "proxy_b", "proxy_c"}; !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("selected ids = %v, want %v", gotIDs, wantIDs)
	}
	if len(got) != 1 || got[0].ID != "proxy_a" || got[0].Name != "代理 A" || got[0].Type != "http" || !got[0].Enabled {
		t.Fatalf("Options() = %+v", got)
	}
}

func TestOptionsRejectsTooManyOrOverlongSelectedIDs(t *testing.T) {
	tooMany := make([]string, 21)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("proxy_%d", index)
	}
	for _, selectedIDs := range [][]string{tooMany, {strings.Repeat("x", 121)}} {
		service := NewService(&proxyOptionStoreStub{})
		_, err := service.Options(context.Background(), OptionListInput{SelectedIDs: selectedIDs})
		if _, ok := ValidationMessage(err); !ok {
			t.Fatalf("Options(%v) error = %v, want validation error", selectedIDs, err)
		}
	}
}

func TestOptionsDefaultsLimit(t *testing.T) {
	store := &proxyOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{Limit: -10}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestOptionsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&proxyOptionStoreStub{err: want})

	_, err := service.Options(context.Background(), OptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("Options() error = %v, want %v", err, want)
	}
}

func TestCreateEncryptsPasswordAndInvalidates(t *testing.T) {
	description := "  说明  "
	username := "  proxy-user  "
	password := "  secret with spaces  "
	enabled := false
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	codec := &proxyCredentialCodecStub{encrypted: "v1:encrypted"}
	invalidator := &proxyInvalidatorStub{}
	store := &proxyOptionStoreStub{
		createResult: port.ManagementProxySummary{
			ID:          "proxy_fixed",
			Name:        "代理 A",
			Description: stringPtr("说明"),
			Type:        "socks5h",
			Host:        "proxy.example.com",
			Port:        1080,
			Username:    stringPtr("proxy-user"),
			Enabled:     false,
			TestStatus:  "unknown",
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:       store,
		Now:         func() time.Time { return now },
		NewID:       func(prefix string) string { return prefix + "_fixed" },
		Codec:       codec,
		Invalidator: invalidator,
	})

	result, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_admin",
		Name:            "  代理 A  ",
		Description:     &description,
		Type:            "socks5h",
		Host:            "  proxy.example.com  ",
		Port:            1080,
		Username:        &username,
		Password:        &password,
		Enabled:         &enabled,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.createInput.ID != "proxy_fixed" ||
		store.createInput.SystemAccountID != "sys_admin" ||
		store.createInput.Name != "代理 A" ||
		store.createInput.Description == nil ||
		*store.createInput.Description != "说明" ||
		store.createInput.Username == nil ||
		*store.createInput.Username != "proxy-user" ||
		store.createInput.PasswordEncrypted == nil ||
		*store.createInput.PasswordEncrypted != "v1:encrypted" ||
		store.createInput.Enabled ||
		!store.createInput.CreatedAt.Equal(now) ||
		!store.createInput.UpdatedAt.Equal(now) {
		t.Fatalf("create input = %+v", store.createInput)
	}
	if codec.password != password {
		t.Fatalf("encrypted password = %q, want original value with spaces", codec.password)
	}
	if !result.PasswordSet || result.Proxy.ID != "proxy_fixed" {
		t.Fatalf("result = %+v", result)
	}
	if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyCreatedReason {
		t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
	}
}

func TestWriteOperationsReturnSuccessWhenInvalidationFails(t *testing.T) {
	wantErr := errors.New("redis down")

	t.Run("create", func(t *testing.T) {
		invalidator := &proxyInvalidatorStub{err: wantErr}
		store := &proxyOptionStoreStub{
			createResult: port.ManagementProxySummary{
				ID:      "proxy_a",
				Name:    "代理 A",
				Type:    "http",
				Host:    "proxy.example.com",
				Port:    8080,
				Enabled: true,
			},
		}
		service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})

		result, err := service.Create(context.Background(), CreateInput{
			SystemAccountID: "sys_admin",
			Name:            "代理 A",
			Type:            "http",
			Host:            "proxy.example.com",
			Port:            8080,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if result.Proxy.ID != "proxy_a" {
			t.Fatalf("result = %+v", result)
		}
		if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyCreatedReason {
			t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
		}
	})

	t.Run("update", func(t *testing.T) {
		invalidator := &proxyInvalidatorStub{err: wantErr}
		store := &proxyOptionStoreStub{
			findResult: port.ManagementProxySummary{
				ID:      "proxy_a",
				Name:    "代理 A",
				Type:    "http",
				Host:    "old.example.com",
				Port:    8080,
				Enabled: true,
			},
			findFound: true,
			updateResult: port.ManagementProxyUpdateResult{
				Before: port.ManagementProxySummary{
					ID:      "proxy_a",
					Name:    "代理 A",
					Type:    "http",
					Host:    "old.example.com",
					Port:    8080,
					Enabled: true,
				},
				Proxy: port.ManagementProxySummary{
					ID:      "proxy_a",
					Name:    "代理 A",
					Type:    "http",
					Host:    "new.example.com",
					Port:    8080,
					Enabled: true,
				},
			},
			updateFound: true,
		}
		service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})
		host := "new.example.com"

		result, err := service.Update(context.Background(), UpdateInput{ID: "proxy_a", Host: &host})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if result.Proxy.Host != "new.example.com" {
			t.Fatalf("result = %+v", result)
		}
		if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyUpdatedReason {
			t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
		}
	})

	t.Run("delete", func(t *testing.T) {
		invalidator := &proxyInvalidatorStub{err: wantErr}
		store := &proxyOptionStoreStub{
			findResult:   port.ManagementProxySummary{ID: "proxy_a", Name: "代理 A"},
			findFound:    true,
			deleteResult: true,
		}
		service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})

		result, err := service.Delete(context.Background(), DeleteInput{ID: "proxy_a"})
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if !result.Deleted {
			t.Fatalf("result = %+v", result)
		}
		if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyDeletedReason {
			t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
		}
	})
}

func TestUpdatePreservesPasswordAndResetsTestStateForConnectionChange(t *testing.T) {
	oldPasswordEncrypted := "v1:old"
	latencyMs := 88
	lastTestedAt := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{
			ID:                "proxy_a",
			Name:              "代理 A",
			Type:              "http",
			Host:              "old.example.com",
			Port:              8080,
			Username:          stringPtr("user"),
			PasswordEncrypted: &oldPasswordEncrypted,
			Enabled:           true,
			TestStatus:        "passed",
			LatencyMs:         &latencyMs,
			LastTestedAt:      &lastTestedAt,
		},
		findFound: true,
		updateResult: port.ManagementProxyUpdateResult{
			Before: port.ManagementProxySummary{
				ID:                "proxy_a",
				Name:              "代理 A",
				Type:              "http",
				Host:              "old.example.com",
				Port:              8080,
				Username:          stringPtr("user"),
				PasswordEncrypted: &oldPasswordEncrypted,
				Enabled:           true,
				TestStatus:        "passed",
				LatencyMs:         &latencyMs,
				LastTestedAt:      &lastTestedAt,
			},
			Proxy: port.ManagementProxySummary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "http",
				Host:       "new.example.com",
				Port:       8080,
				Username:   stringPtr("user"),
				Enabled:    true,
				TestStatus: "unknown",
			},
			ResetTestState: true,
		},
		updateFound: true,
	}
	invalidator := &proxyInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:       store,
		Now:         func() time.Time { return time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC) },
		Invalidator: invalidator,
	})
	host := " new.example.com "

	result, err := service.Update(context.Background(), UpdateInput{ID: "proxy_a", Host: &host})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.updateInput.PasswordEncrypted != nil ||
		store.updateInput.PasswordEncryptedWasChanged ||
		store.updateInput.Host == nil ||
		*store.updateInput.Host != "new.example.com" {
		t.Fatalf("update input = %+v", store.updateInput)
	}
	if !result.Changed || result.PasswordChanged || !result.ResetTestState {
		t.Fatalf("result = %+v", result)
	}
	if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyUpdatedReason {
		t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
	}
}

func TestUpdateDoesNotUseUnlockedPreRead(t *testing.T) {
	store := &proxyOptionStoreStub{
		findErr: errors.New("unlocked pre-read must not be used"),
		updateResult: port.ManagementProxyUpdateResult{
			Before: port.ManagementProxySummary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "http",
				Host:       "old.example.com",
				Port:       8080,
				Enabled:    true,
				TestStatus: "passed",
			},
			Proxy: port.ManagementProxySummary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "http",
				Host:       "new.example.com",
				Port:       8080,
				Enabled:    true,
				TestStatus: "unknown",
			},
			ResetTestState: true,
		},
		updateFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store})
	host := "new.example.com"

	result, err := service.Update(context.Background(), UpdateInput{ID: "proxy_a", Host: &host})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Proxy.Host != "new.example.com" {
		t.Fatalf("result = %+v", result)
	}
	if store.findID != "" {
		t.Fatalf("FindManagementProxy() id = %q, want no unlocked pre-read", store.findID)
	}
}

func TestUpdatePasswordEncryptsAndResetsTestState(t *testing.T) {
	codec := &proxyCredentialCodecStub{encrypted: "v1:new"}
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{
			ID:         "proxy_a",
			Name:       "代理 A",
			Type:       "http",
			Host:       "proxy.example.com",
			Port:       8080,
			Enabled:    true,
			TestStatus: "passed",
		},
		findFound: true,
		updateResult: port.ManagementProxyUpdateResult{
			Before: port.ManagementProxySummary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "http",
				Host:       "proxy.example.com",
				Port:       8080,
				Enabled:    true,
				TestStatus: "passed",
			},
			Proxy: port.ManagementProxySummary{
				ID:         "proxy_a",
				Name:       "代理 A",
				Type:       "http",
				Host:       "proxy.example.com",
				Port:       8080,
				Enabled:    true,
				TestStatus: "unknown",
			},
			ResetTestState: true,
		},
		updateFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Codec: codec})
	password := "  updated secret  "

	result, err := service.Update(context.Background(), UpdateInput{ID: "proxy_a", Password: &password})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.updateInput.PasswordEncrypted == nil ||
		*store.updateInput.PasswordEncrypted != "v1:new" ||
		!store.updateInput.PasswordEncryptedWasChanged {
		t.Fatalf("update input = %+v", store.updateInput)
	}
	if codec.password != password {
		t.Fatalf("encrypted password = %q, want original value", codec.password)
	}
	if !result.PasswordChanged || !result.ResetTestState {
		t.Fatalf("result = %+v", result)
	}
}

func TestDeleteRejectsBoundProxyWithWindowMessage(t *testing.T) {
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{ID: "proxy_a", Name: "代理 A"},
		findFound:  true,
		bindings: []port.ManagementProxyAccountBinding{
			{ID: "acct_1", Name: "账户 1"},
			{ID: "acct_2", Name: "账户 2"},
			{ID: "acct_3", Name: "账户 3"},
			{ID: "acct_4", Name: "账户 4"},
		},
		deleteResult: true,
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store})

	_, err := service.Delete(context.Background(), DeleteInput{ID: "proxy_a"})
	if err == nil {
		t.Fatal("Delete() error = nil, want in-use error")
	}
	message, ok := InUseMessage(err)
	if !ok || !strings.Contains(message, "至少 4 个账户") || !strings.Contains(message, "账户 1、账户 2、账户 3 等") {
		t.Fatalf("in-use message = %q ok=%v", message, ok)
	}
	if store.deleteCalled {
		t.Fatal("DeleteManagementProxy should not be called while proxy is bound")
	}
	if store.bindingsInput.Limit != proxyUsageWindowLimit {
		t.Fatalf("bindings input = %+v", store.bindingsInput)
	}
}

func TestDeleteRemovesProxyAndInvalidates(t *testing.T) {
	invalidator := &proxyInvalidatorStub{}
	store := &proxyOptionStoreStub{
		findResult:   port.ManagementProxySummary{ID: "proxy_a", Name: "代理 A"},
		findFound:    true,
		deleteResult: true,
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})

	result, err := service.Delete(context.Background(), DeleteInput{ID: " proxy_a "})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !result.Deleted || result.Before.ID != "proxy_a" || !store.deleteCalled || store.deleteID != "proxy_a" {
		t.Fatalf("result = %+v deleteID=%q called=%v", result, store.deleteID, store.deleteCalled)
	}
	if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyDeletedReason {
		t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
	}
}

func TestDeleteMapsConcurrentBindingConflict(t *testing.T) {
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{ID: "proxy_a", Name: "代理 A"},
		findFound:  true,
		deleteErr:  port.ErrManagementProxyInUse,
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store})

	_, err := service.Delete(context.Background(), DeleteInput{ID: "proxy_a"})

	message, ok := InUseMessage(err)
	if !ok || !strings.Contains(message, "至少 1 个账户") {
		t.Fatalf("InUseMessage() = %q ok=%v, want concurrent binding conflict", message, ok)
	}
}

func TestTestBuildsReportAndPersistsState(t *testing.T) {
	testedAt := time.Date(2026, 7, 10, 10, 30, 0, 0, time.UTC)
	encrypted := "v1:encrypted"
	staleOutboundIP := "198.51.100.5"
	staleOutboundRegion := "旧地区"
	latencyMs := 105
	outboundIP := "203.0.113.10"
	outboundRegion := "美国"
	message := "代理可用，存在 1 项告警"
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{
			ID:                "proxy_a",
			Name:              "代理 A",
			Type:              "http",
			Host:              "proxy.example.com",
			Port:              8080,
			Username:          stringPtr("proxy-user"),
			PasswordEncrypted: &encrypted,
			Enabled:           false,
			TestStatus:        "unknown",
			OutboundIP:        &staleOutboundIP,
			OutboundRegion:    &staleOutboundRegion,
		},
		findFound: true,
		providers: []port.ManagementProviderOption{
			{Code: "openai", Name: "OpenAI", Enabled: true, BaseURL: "https://api.openai.com/v1"},
			{Code: "vendor", Name: "Vendor", Enabled: true, BaseURL: "https://vendor.example.com"},
			{Code: "disabled", Name: "Disabled", Enabled: false, BaseURL: "https://disabled.example.com"},
		},
		testStateResult: port.ManagementProxySummary{
			ID:              "proxy_a",
			Name:            "代理 A",
			Type:            "http",
			Host:            "proxy.example.com",
			Port:            8080,
			Enabled:         false,
			TestStatus:      "warning",
			LatencyMs:       &latencyMs,
			OutboundIP:      &outboundIP,
			OutboundRegion:  &outboundRegion,
			LastTestMessage: &message,
			LastTestedAt:    &testedAt,
		},
		testStateFound: true,
	}
	probe := &proxyProbeStub{
		results: map[string]ProxyProbeResult{
			"http://ip-api.com/json/?lang=zh-CN": {
				StatusCode: 200,
				LatencyMs:  50,
				Body:       `{"status":"success","query":"203.0.113.10","country":"United States","countryCode":"US","regionName":"CA","city":"San Francisco"}`,
			},
			"https://api.openai.com/v1": {
				StatusCode: 200,
				LatencyMs:  120,
			},
			"https://vendor.example.com": {
				StatusCode: 401,
				LatencyMs:  90,
			},
		},
	}
	codec := &proxyCredentialCodecStub{
		decrypted: map[string]any{"password": "p@ss word"},
	}
	invalidator := &proxyInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:       store,
		Codec:       codec,
		Probe:       probe,
		Now:         func() time.Time { return testedAt },
		Invalidator: invalidator,
	})

	result, err := service.Test(context.Background(), TestInput{ID: "proxy_a"})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	report := result.Report
	if report.ProxyID != "proxy_a" ||
		report.ProxyName != "代理 A" ||
		report.Status != "warning" ||
		report.Score != 90 ||
		report.Grade != "A" ||
		report.PassedCount != 2 ||
		report.WarningCount != 1 ||
		report.FailedCount != 0 ||
		report.BaseLatencyMs == nil ||
		*report.BaseLatencyMs != 105 ||
		report.OutboundIP == nil ||
		*report.OutboundIP != "203.0.113.10" ||
		report.OutboundRegion == nil ||
		*report.OutboundRegion == "" ||
		report.Message != message ||
		report.TestedAt != testedAt.Format(time.RFC3339Nano) {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Items) != 3 ||
		report.Items[0].Name != "基础连通性" ||
		report.Items[0].Status != "passed" ||
		report.Items[1].HTTPStatus == nil ||
		*report.Items[1].HTTPStatus != 200 ||
		report.Items[2].Status != "warning" {
		t.Fatalf("items = %+v", report.Items)
	}
	if store.testStateInput.ID != "proxy_a" ||
		store.testStateInput.TestStatus != "warning" ||
		store.testStateInput.LatencyMs == nil ||
		*store.testStateInput.LatencyMs != 105 ||
		!store.testStateInput.OutboundIP.Set ||
		store.testStateInput.OutboundIP.Value == nil ||
		*store.testStateInput.OutboundIP.Value != "203.0.113.10" ||
		!store.testStateInput.OutboundRegion.Set ||
		store.testStateInput.LastTestMessage != message ||
		!store.testStateInput.LastTestedAt.Equal(testedAt) {
		t.Fatalf("test state input = %+v", store.testStateInput)
	}
	if len(probe.inputs) != 3 {
		t.Fatalf("probe inputs = %+v", probe.inputs)
	}
	for _, input := range probe.inputs {
		if input.ProxyURL != "http://proxy-user:p%40ss%20word@proxy.example.com:8080" {
			t.Fatalf("proxy URL = %q", input.ProxyURL)
		}
		if strings.Contains(input.ProxyURL, "v1:encrypted") {
			t.Fatalf("proxy URL leaked encrypted credential: %q", input.ProxyURL)
		}
	}
	if result.Before.OutboundIP == nil ||
		*result.Before.OutboundIP != staleOutboundIP ||
		result.Proxy.TestStatus != "warning" {
		t.Fatalf("result = %+v", result)
	}
	if len(invalidator.reasons) != 1 || invalidator.reasons[0] != ProxyTestStateUpdatedReason {
		t.Fatalf("invalidation reasons = %+v", invalidator.reasons)
	}
}

func TestTestPreservesStoredOutboundWhenProbeCannotResolveIt(t *testing.T) {
	testedAt := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	staleOutboundIP := "198.51.100.5"
	staleOutboundRegion := "旧地区"
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{
			ID:             "proxy_a",
			Name:           "代理 A",
			Type:           "http",
			Host:           "proxy.example.com",
			Port:           8080,
			OutboundIP:     &staleOutboundIP,
			OutboundRegion: &staleOutboundRegion,
		},
		findFound: true,
		providers: []port.ManagementProviderOption{
			{Code: "openai", Name: "OpenAI", Enabled: true, BaseURL: "https://api.openai.com/v1"},
		},
		testStateResult: port.ManagementProxySummary{
			ID:             "proxy_a",
			Name:           "代理 A",
			Type:           "http",
			Host:           "proxy.example.com",
			Port:           8080,
			TestStatus:     "passed",
			OutboundIP:     &staleOutboundIP,
			OutboundRegion: &staleOutboundRegion,
			LastTestedAt:   &testedAt,
		},
		testStateFound: true,
	}
	probe := &proxyProbeStub{
		results: map[string]ProxyProbeResult{
			"https://api.openai.com/v1": {StatusCode: 204, LatencyMs: 20},
		},
		errs: map[string]error{
			"http://ip-api.com/json/?lang=zh-CN": errors.New("outbound unavailable"),
			"https://ipwho.is/":                  errors.New("outbound unavailable"),
			"https://api.ip.sb/geoip":            errors.New("outbound unavailable"),
			"https://ipinfo.io/json":             errors.New("outbound unavailable"),
			"https://api.ipify.org?format=json":  errors.New("outbound unavailable"),
			"http://httpbin.org/ip":              errors.New("outbound unavailable"),
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Probe: probe,
		Now:   func() time.Time { return testedAt },
	})

	result, err := service.Test(context.Background(), TestInput{ID: "proxy_a"})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if result.Report.OutboundIP != nil || result.Report.OutboundRegion != nil {
		t.Fatalf("report outbound = %v / %v, want omitted", result.Report.OutboundIP, result.Report.OutboundRegion)
	}
	if store.testStateInput.OutboundIP.Set || store.testStateInput.OutboundRegion.Set {
		t.Fatalf("test state input = %+v, want stored outbound preserved", store.testStateInput)
	}
}

func TestTestReturnsNotFoundBeforeProbing(t *testing.T) {
	store := &proxyOptionStoreStub{}
	probe := &proxyProbeStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Probe: probe})

	_, err := service.Test(context.Background(), TestInput{ID: "missing"})

	if !errors.Is(err, ErrProxyNotFound) {
		t.Fatalf("Test() error = %v, want ErrProxyNotFound", err)
	}
	if len(probe.inputs) != 0 {
		t.Fatalf("probe inputs = %+v, want none", probe.inputs)
	}
}

func TestTestStartsDeadlineBeforeListingProviders(t *testing.T) {
	store := &proxyOptionStoreStub{
		findResult: port.ManagementProxySummary{
			ID:      "proxy_a",
			Name:    "代理 A",
			Type:    "http",
			Host:    "proxy.example.com",
			Port:    8080,
			Enabled: true,
		},
		findFound: true,
		testStateResult: port.ManagementProxySummary{
			ID:         "proxy_a",
			Name:       "代理 A",
			Type:       "http",
			Host:       "proxy.example.com",
			Port:       8080,
			Enabled:    true,
			TestStatus: "failed",
		},
		testStateFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Probe: &proxyProbeStub{},
	})

	if _, err := service.Test(context.Background(), TestInput{ID: "proxy_a"}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if !store.providerDeadlineSet {
		t.Fatal("ListManagementProviders() context has no manual test deadline")
	}
}

func TestProviderProbeTimeoutUsesProbeTimeoutMessage(t *testing.T) {
	targetURL := "https://api.openai.com/v1"
	service := NewServiceWithOptions(ServiceOptions{
		Probe: &proxyProbeStub{
			errs: map[string]error{targetURL: context.DeadlineExceeded},
		},
	})

	item := service.testProvider(context.Background(), "http://proxy.example.com:8080", port.ManagementProviderOption{
		Code:    "openai",
		Name:    "OpenAI",
		Enabled: true,
		BaseURL: targetURL,
	})

	if item.Message != "代理检测请求超时" {
		t.Fatalf("message = %q, want per-probe timeout message", item.Message)
	}
}

func TestProxyURLIgnoresPasswordWithoutUsername(t *testing.T) {
	encrypted := "v1:encrypted"
	service := NewServiceWithOptions(ServiceOptions{
		Codec: &proxyCredentialCodecStub{
			decrypted: map[string]any{"password": "secret"},
		},
	})

	got, err := service.proxyURL(port.ManagementProxySummary{
		Type:              "http",
		Host:              "proxy.example.com",
		Port:              8080,
		PasswordEncrypted: &encrypted,
	})
	if err != nil {
		t.Fatalf("proxyURL() error = %v", err)
	}
	if got != "http://proxy.example.com:8080" {
		t.Fatalf("proxyURL() = %q, want password ignored without username", got)
	}
}

type proxyOptionStoreStub struct {
	listInput           port.ManagementProxyListInput
	input               port.ManagementProxyOptionListInput
	listResult          port.ManagementProxyListResult
	options             []port.ManagementProxyOption
	listErr             error
	err                 error
	findID              string
	findResult          port.ManagementProxySummary
	findFound           bool
	findErr             error
	createInput         port.ManagementProxyCreateInput
	createResult        port.ManagementProxySummary
	createErr           error
	updateInput         port.ManagementProxyUpdateInput
	updateResult        port.ManagementProxyUpdateResult
	updateFound         bool
	updateErr           error
	bindingsInput       port.ManagementProxyAccountBindingListInput
	bindings            []port.ManagementProxyAccountBinding
	bindingsErr         error
	deleteCalled        bool
	deleteID            string
	deleteResult        bool
	deleteErr           error
	providerInput       port.ManagementProviderListInput
	providers           []port.ManagementProviderOption
	providerErr         error
	providerDeadlineSet bool
	testStateInput      port.ManagementProxyTestStateInput
	testStateResult     port.ManagementProxySummary
	testStateFound      bool
	testStateErr        error
}

func (s *proxyOptionStoreStub) ListManagementProxies(_ context.Context, input port.ManagementProxyListInput) (port.ManagementProxyListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *proxyOptionStoreStub) ListManagementProxyOptions(_ context.Context, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	s.input = input
	return s.options, s.err
}

func (s *proxyOptionStoreStub) FindManagementProxy(_ context.Context, id string) (port.ManagementProxySummary, bool, error) {
	s.findID = id
	return s.findResult, s.findFound, s.findErr
}

func (s *proxyOptionStoreStub) CreateManagementProxy(_ context.Context, input port.ManagementProxyCreateInput) (port.ManagementProxySummary, error) {
	s.createInput = input
	return s.createResult, s.createErr
}

func (s *proxyOptionStoreStub) UpdateManagementProxy(_ context.Context, input port.ManagementProxyUpdateInput) (port.ManagementProxyUpdateResult, bool, error) {
	s.updateInput = input
	return s.updateResult, s.updateFound, s.updateErr
}

func (s *proxyOptionStoreStub) ListManagementProxyAccountBindings(_ context.Context, input port.ManagementProxyAccountBindingListInput) ([]port.ManagementProxyAccountBinding, error) {
	s.bindingsInput = input
	return s.bindings, s.bindingsErr
}

func (s *proxyOptionStoreStub) DeleteManagementProxy(_ context.Context, id string) (bool, error) {
	s.deleteCalled = true
	s.deleteID = id
	return s.deleteResult, s.deleteErr
}

func (s *proxyOptionStoreStub) ListManagementProviders(ctx context.Context, input port.ManagementProviderListInput) ([]port.ManagementProviderOption, error) {
	s.providerInput = input
	_, s.providerDeadlineSet = ctx.Deadline()
	return s.providers, s.providerErr
}

func (s *proxyOptionStoreStub) UpdateManagementProxyTestState(_ context.Context, input port.ManagementProxyTestStateInput) (port.ManagementProxySummary, bool, error) {
	s.testStateInput = input
	return s.testStateResult, s.testStateFound, s.testStateErr
}

type proxyCredentialCodecStub struct {
	encrypted string
	password  string
	decrypted map[string]any
	err       error
}

func (s *proxyCredentialCodecStub) EncryptJSON(value map[string]any) (string, error) {
	password, _ := value["password"].(string)
	s.password = password
	if s.err != nil {
		return "", s.err
	}
	return s.encrypted, nil
}

func (s *proxyCredentialCodecStub) DecryptJSON(_ string) (map[string]any, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.decrypted, nil
}

type proxyProbeStub struct {
	mu      sync.Mutex
	inputs  []ProxyProbeInput
	results map[string]ProxyProbeResult
	errs    map[string]error
}

func (s *proxyProbeStub) Probe(_ context.Context, input ProxyProbeInput) (ProxyProbeResult, error) {
	s.mu.Lock()
	s.inputs = append(s.inputs, input)
	s.mu.Unlock()
	if err := s.errs[input.TargetURL]; err != nil {
		return ProxyProbeResult{}, err
	}
	return s.results[input.TargetURL], nil
}

type proxyInvalidatorStub struct {
	reasons []string
	err     error
}

func (s *proxyInvalidatorStub) InvalidateProxyChanged(_ context.Context, reason string) error {
	s.reasons = append(s.reasons, reason)
	return s.err
}

func stringPtr(value string) *string {
	return &value
}
