package main

import "testing"

// T221 契约钉值：qwen 媒体任务（万相视频 / paraformer 长转写）出站链对
// base 尾部 DashScope 服务根先剥一次再按全路径 opPath 直拼，与 chat/向量/
// 重排链对齐——连接域写面已按全路径模板前缀拒绝带前缀 base（钉值
// TestB1ValidateConnectionBaseURLTable），剥根兜底存量行与凭据域 base，
// 避免 …/compatible-mode/v1/api/v1/… 双根 404。
func TestChainQwenJoinUpstreamURLStripsBaseServiceRoot(t *testing.T) {
	cases := []struct {
		name, base, path, want string
	}{
		{
			name: "official origin stays untouched",
			base: "https://dashscope.aliyuncs.com",
			path: "/api/v1/services/aigc/video-generation/video-synthesis",
			want: "https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis",
		},
		{
			name: "compatible-mode suffix stripped once",
			base: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			path: "/api/v1/services/aigc/video-generation/video-synthesis",
			want: "https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis",
		},
		{
			name: "api-v1 suffix stripped once",
			base: "https://dashscope.aliyuncs.com/api/v1/",
			path: "/api/v1/tasks/task-123",
			want: "https://dashscope.aliyuncs.com/api/v1/tasks/task-123",
		},
		{
			name: "similar suffix is not stripped",
			base: "https://dashscope.aliyuncs.com/api/v11",
			path: "/api/v1/tasks/task-123",
			want: "https://dashscope.aliyuncs.com/api/v11/api/v1/tasks/task-123",
		},
		{
			name: "custom proxy prefix is kept",
			base: "https://qwen-proxy.example.com/dashscope",
			path: "/api/v1/tasks/task-123",
			want: "https://qwen-proxy.example.com/dashscope/api/v1/tasks/task-123",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainQwenJoinUpstreamURL(tc.base, tc.path)
			if err != nil {
				t.Fatalf("chainQwenJoinUpstreamURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("chainQwenJoinUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}

	if got := chainQwenStripBaseServiceRoot("https://[bad"); got != "https://[bad" {
		t.Fatalf("解析失败必须原样返回: %q", got)
	}
}
