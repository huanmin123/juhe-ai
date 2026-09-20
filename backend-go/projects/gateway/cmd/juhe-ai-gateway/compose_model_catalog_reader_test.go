package main

// accountModelCatalogFacts（providers.ModelCatalogItem →
// accounts.AccountModelCatalogFact 投影）的单元覆盖：四列直接拷贝、空列表、
// 多行互不串扰。适配器对 ListProviderModelsForRequest 的参数透传见 compose.go
// 适配器注释（includeInactive 恒 false，includeUnpriced 原样透传）。

import (
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
)

func TestAccountModelCatalogFacts(t *testing.T) {
	cases := []struct {
		name  string
		items []providers.ModelCatalogItem
		want  []accounts.AccountModelCatalogFact
	}{
		{
			name:  "空列表返回空 fact 列",
			items: []providers.ModelCatalogItem{},
			want:  []accounts.AccountModelCatalogFact{},
		},
		{
			name: "nil 列表与空列表等价",
			want: []accounts.AccountModelCatalogFact{},
		},
		{
			name: "单行四列直接拷贝",
			items: []providers.ModelCatalogItem{
				{
					Model:                     "gpt-4.1",
					SupportedAPIProtocols:     []string{"chat_completions", "responses"},
					SupportedServiceTiers:     []string{"auto", "flex"},
					SupportedReasoningEfforts: []string{"low", "high"},
				},
			},
			want: []accounts.AccountModelCatalogFact{
				{
					Model:                     "gpt-4.1",
					SupportedAPIProtocols:     []string{"chat_completions", "responses"},
					SupportedServiceTiers:     []string{"auto", "flex"},
					SupportedReasoningEfforts: []string{"low", "high"},
				},
			},
		},
		{
			name: "多行各自拷贝互不串扰",
			items: []providers.ModelCatalogItem{
				{
					Model:                     "gemini-2.5-pro",
					SupportedAPIProtocols:     []string{"generate_content"},
					SupportedServiceTiers:     []string{},
					SupportedReasoningEfforts: []string{"medium"},
				},
				{
					Model:                     "claude-sonnet-4",
					SupportedAPIProtocols:     []string{"messages"},
					SupportedServiceTiers:     []string{"standard"},
					SupportedReasoningEfforts: nil,
				},
			},
			want: []accounts.AccountModelCatalogFact{
				{
					Model:                     "gemini-2.5-pro",
					SupportedAPIProtocols:     []string{"generate_content"},
					SupportedServiceTiers:     []string{},
					SupportedReasoningEfforts: []string{"medium"},
				},
				{
					Model:                     "claude-sonnet-4",
					SupportedAPIProtocols:     []string{"messages"},
					SupportedServiceTiers:     []string{"standard"},
					SupportedReasoningEfforts: nil,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := accountModelCatalogFacts(tc.items)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fact 投影不一致：got=%#v want=%#v", got, tc.want)
			}
		})
	}
}
