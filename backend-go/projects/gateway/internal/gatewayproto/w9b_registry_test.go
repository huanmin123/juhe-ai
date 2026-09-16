package gatewayproto

// w9b 注册表面补充：Drivers 快照、按请求/档案解析、路径识别与证据构造。

import (
	"errors"
	"net/http"
	"testing"
)

type w9bStubDriver struct {
	profileID        string
	responseProtocol string
	matchPath        bool
	endpointMode     EndpointMode
}

func (d *w9bStubDriver) SupportsProfile(profile ProtocolProfile) bool {
	return profile.ID == d.profileID
}
func (d *w9bStubDriver) ResponseProtocol() string { return d.responseProtocol }
func (d *w9bStubDriver) MatchPath(shape RequestShape) bool {
	return d.matchPath && shape.Path == "/v1/chat/completions"
}
func (d *w9bStubDriver) EndpointModeForRequestShape(shape RequestShape) (EndpointMode, bool) {
	return d.endpointMode, d.endpointMode != ""
}

func (d *w9bStubDriver) ID() string                   { return d.profileID }
func (d *w9bStubDriver) ProtocolCode() string         { return "openai" }
func (d *w9bStubDriver) ProtocolVersion() string      { return "v1" }
func (d *w9bStubDriver) ClientErrorProtocol() string  { return "openai" }
func (d *w9bStubDriver) DefaultClientProfile() string { return d.profileID }
func (d *w9bStubDriver) BuildUpstreamRequest(input BuildUpstreamRequestInput) (*BuildUpstreamRequestResult, error) {
	return nil, errors.New("w9b stub")
}
func (d *w9bStubDriver) NewStreamInspector() StreamInspector { return nil }
func (d *w9bStubDriver) InspectResponse(input InspectResponseInput) ResponseInspection {
	return ResponseInspection{}
}
func (d *w9bStubDriver) ExtractUsageFromJSONBuffer(body []byte) ParsedUsage {
	return ParsedUsage{}
}
func (d *w9bStubDriver) ExtractUsageFromJSONValue(value any) ParsedUsage {
	return ParsedUsage{}
}
func (d *w9bStubDriver) ExtractUsageFromJSONTextFragment(text string) ParsedUsage {
	return ParsedUsage{}
}
func (d *w9bStubDriver) ParseErrorPayload(bodyText string, header http.Header) ErrorPayload {
	return ErrorPayload{}
}

func TestW9BRegistryDriversSnapshot(t *testing.T) {
	driver := &w9bStubDriver{profileID: "openai"}
	registry := NewRegistry(driver)
	drivers := registry.Drivers()
	if len(drivers) != 1 {
		t.Fatalf("drivers=%d", len(drivers))
	}
	// 返回副本：修改切片不影响注册表。
	registry.Drivers()[0] = nil
	if len(registry.Drivers()) != 1 || registry.Drivers()[0] == nil {
		t.Fatal("Drivers 必须返回副本")
	}
}

func TestW9BDriverForRequestOrProfileArms(t *testing.T) {
	driver := &w9bStubDriver{profileID: "openai", matchPath: true, endpointMode: EndpointMode("chat")}
	registry := NewRegistry(driver)
	// 路径命中。
	resolved, err := registry.DriverForRequestOrProfile(RequestShape{Path: "/v1/chat/completions"}, ProtocolProfile{})
	if err != nil || resolved != driver {
		t.Fatalf("路径命中=%v err=%v", resolved, err)
	}
	// 路径未命中但档案命中。
	resolved, err = registry.DriverForRequestOrProfile(RequestShape{Path: "/other"}, ProtocolProfile{ID: "openai"})
	if err != nil || resolved != driver {
		t.Fatalf("档案命中=%v err=%v", resolved, err)
	}
	// 双双未命中 → 错误。
	if _, err := registry.DriverForRequestOrProfile(RequestShape{Path: "/other"}, ProtocolProfile{ID: "anthropic"}); err == nil {
		t.Fatal("未命中必须报错")
	}
	// 空 profile ID 的错误文案。
	if _, err := registry.DriverForRequestOrProfile(RequestShape{Path: "/other"}, ProtocolProfile{}); err == nil || errors.Is(err, errors.New("")) && err.Error() == "" {
		t.Fatalf("空档案错误=%v", err)
	}
}

func TestW9BIsProtocolRequestPath(t *testing.T) {
	driver := &w9bStubDriver{profileID: "openai", matchPath: true}
	registry := NewRegistry(driver)
	if !registry.IsProtocolRequestPath(RequestShape{Path: "/v1/chat/completions"}) {
		t.Fatal("驱动识别的路径必须为协议请求")
	}
	if registry.IsProtocolRequestPath(RequestShape{Path: "/admin"}) {
		t.Fatal("未知路径不应识别")
	}
}

func TestW9BAttemptEvidenceShape(t *testing.T) {
	inspection := ResponseInspection{SemanticSuccess: true, ProtocolComplete: true}
	evidence := inspection.AttemptEvidence(200)
	if evidence.StatusCode != 200 || !evidence.SemanticSuccess {
		t.Fatalf("evidence=%+v", evidence)
	}
}

func TestW9BBuildUpstreamErrorMessage(t *testing.T) {
	err := &BuildUpstreamError{Code: "build_failed", Message: "上游构造失败"}
	if err.Error() != "上游构造失败" {
		t.Fatalf("Error=%q", err.Error())
	}
}
