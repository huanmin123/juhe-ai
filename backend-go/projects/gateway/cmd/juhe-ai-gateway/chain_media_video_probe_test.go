package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func probeTestMP4Box(boxType string, payload []byte) []byte {
	box := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(box[0:4], uint32(8+len(payload)))
	copy(box[4:8], boxType)
	return append(box, payload...)
}

func probeTestMP4(timescale, duration uint32, mdatSize int) []byte {
	ftyp := probeTestMP4Box("ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
	mvhdPayload := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], timescale)
	binary.BigEndian.PutUint32(mvhdPayload[16:20], duration)
	moov := probeTestMP4Box("moov", probeTestMP4Box("mvhd", mvhdPayload))
	mdat := probeTestMP4Box("mdat", make([]byte, mdatSize))
	return append(append(append([]byte{}, ftyp...), mdat...), moov...)
}

func probeTestChain(security sharedupstreamhttp.URLSecurityConfig) *gatewayChain {
	return &gatewayChain{
		mediaJobs: &mediaJobsRuntime{
			transport: gatewayupstream.TransportDeps{
				DialGuard: sharedupstreamhttp.NewDialGuard(security, nil, nil),
			},
		},
	}
}

func probeTestRecord(artifactURL string) (*gatewaymedia.MediaJobRecord, *gatewaymedia.MediaJobIR, *chainMediaJobAccount) {
	seconds := 0.0
	_ = seconds
	record := &gatewaymedia.MediaJobRecord{
		ID:         "job-probe",
		Kind:       gatewaymedia.JobKindVideo,
		VendorCode: "glm",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model: "cogvideox-3",
			MediaPricing: &mediapricing.Pricing{
				Version:         1,
				Unit:            mediapricing.UnitSecond,
				BaseUsd:         0.1,
				BaseTier:        mediapricing.VideoTier720P,
				TierMultipliers: mediapricing.DefaultVideoTierMultipliers,
			},
		},
	}
	ir := &gatewaymedia.MediaJobIR{
		Status:   gatewaymedia.JobStatusCompleted,
		Artifact: gatewaymedia.MediaJobArtifact{ContentURL: artifactURL},
	}
	account := &chainMediaJobAccount{ID: "acct-1", ProviderCode: "glm"}
	return record, ir, account
}

func TestChainMediaJobProbeVideoSecondsUsesRangeAndSettlement(t *testing.T) {
	data := probeTestMP4(1000, 2500, 7<<20)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("probe must not carry Authorization: %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "" {
			t.Fatalf("probe must not carry Cookie: %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "artifact.mp4", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	settlement := chain.chainMediaJobTerminalSettlement(context.Background(), account, record, ir)
	if settlement.usageMissing || settlement.dimension != chainMediaJobDimensionVideoSeconds {
		t.Fatalf("settlement = %+v, want video seconds", settlement)
	}
	if settlement.quantitySource != "gateway_parse" || settlement.quantity != 2.5 {
		t.Fatalf("settlement quantity = %s/%v, want gateway_parse/2.5", settlement.quantitySource, settlement.quantity)
	}
	if ir.Usage.OutputVideoSeconds == nil || *ir.Usage.OutputVideoSeconds != 2.5 {
		t.Fatalf("ir usage seconds = %v, want 2.5", ir.Usage.OutputVideoSeconds)
	}
	if requests < 2 {
		t.Fatalf("requests = %d, want head+tail range requests", requests)
	}
}

func TestChainMediaJobProbeFollowsAtMostThreeRedirects(t *testing.T) {
	data := probeTestMP4(1000, 1500, 7<<20)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "artifact.mp4", time.Time{}, bytes.NewReader(data))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/artifact.mp4", http.StatusFound)
	}))
	defer redirect.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(redirect.URL + "/artifact.mp4")
	seconds, ok := chain.chainMediaJobProbeVideoSeconds(context.Background(), account, record, ir)
	if !ok || seconds != 1.5 {
		t.Fatalf("probe = %v/%v, want 1.5", seconds, ok)
	}
}

func TestChainMediaJobProbeRejectsFourthRedirect(t *testing.T) {
	var requests int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, server.URL+"/artifact.mp4", http.StatusFound)
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	if seconds, ok := chain.chainMediaJobProbeVideoSeconds(context.Background(), account, record, ir); ok {
		t.Fatalf("probe = %v, want redirect limit failure", seconds)
	}
	if requests > 4 {
		t.Fatalf("requests = %d, redirect limit not enforced", requests)
	}
}

func TestChainMediaJobProbeBlocksPrivateRedirect(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests++
	}))
	defer target.Close()
	initial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+"/artifact.mp4", http.StatusFound)
	}))
	defer initial.Close()

	// 只放行初始 origin；重定向到另一私网 origin 必须被 CheckRedirect 的
	// DialGuard.ValidateHost 阻断（AC-028）。
	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{
		PrivateOriginAllowlist: map[string]bool{initial.URL: true},
	})
	record, ir, account := probeTestRecord(initial.URL + "/artifact.mp4")
	if seconds, ok := chain.chainMediaJobProbeVideoSeconds(context.Background(), account, record, ir); ok {
		t.Fatalf("probe = %v, want private redirect blocked", seconds)
	}
	if targetRequests != 0 {
		t.Fatalf("private redirect target reached %d times", targetRequests)
	}
}

func TestChainMediaJobProbeBlocksPrivateArtifact(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	if seconds, ok := chain.chainMediaJobProbeVideoSeconds(context.Background(), account, record, ir); ok {
		t.Fatalf("probe = %v, want private artifact blocked", seconds)
	}
	if requests != 0 {
		t.Fatalf("private artifact reached server %d times", requests)
	}
}

func TestChainMediaJobProbeSkippedWhenRequestSecondsApplied(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	seconds := 4.0
	record.RequestSnapshot.Seconds = &seconds
	record.ParamsApplied = []string{"seconds"}

	settlement := chain.chainMediaJobTerminalSettlement(context.Background(), account, record, ir)
	if settlement.usageMissing || settlement.quantitySource != "request" || settlement.quantity != 4 {
		t.Fatalf("settlement = %+v, want request/4", settlement)
	}
	if requests != 0 {
		t.Fatalf("request seconds should win over probe, got %d probe requests", requests)
	}
}

func TestChainMediaJobProbeSkippedWhenRequestDurationApplied(t *testing.T) {
	// duration 与 seconds 同义：ParamsApplied 记录用户原始词 duration 时，
	// 请求时长同样构成计量基源（不得因词表差异回落探测/usage_missing）。
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	seconds := 5.0
	record.RequestSnapshot.Seconds = &seconds
	record.ParamsApplied = []string{"duration"}

	settlement := chain.chainMediaJobTerminalSettlement(context.Background(), account, record, ir)
	if settlement.usageMissing || settlement.quantitySource != "request" || settlement.quantity != 5 {
		t.Fatalf("settlement = %+v, want request/5", settlement)
	}
	if requests != 0 {
		t.Fatalf("request duration should win over probe, got %d probe requests", requests)
	}
}

func TestChainMediaJobProbeFailsClosedOnRangeIgnored(t *testing.T) {
	data := probeTestMP4(1000, 1500, 7<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(data)
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	if seconds, ok := chain.chainMediaJobProbeVideoSeconds(context.Background(), account, record, ir); ok {
		t.Fatalf("probe = %v, want range-unsupported failure", seconds)
	}
	settlement := chain.chainMediaJobTerminalSettlement(context.Background(), account, record, ir)
	if !settlement.usageMissing || settlement.costUSD != 0 {
		t.Fatalf("settlement = %+v, want usage_missing/0", settlement)
	}
}

func TestChainMediaJobProbeHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if seconds, ok := chain.chainMediaJobProbeVideoSeconds(ctx, account, record, ir); ok {
		t.Fatalf("probe = %v, want timeout failure", seconds)
	}
}

func TestChainMediaJobProbeFailsClosedWithoutDialGuard(t *testing.T) {
	chain := &gatewayChain{mediaJobs: &mediaJobsRuntime{}}
	record, ir, account := probeTestRecord("https://cdn.example.com/artifact.mp4")
	if seconds, ok := chain.chainMediaJobProbeVideoSeconds(context.Background(), account, record, ir); ok {
		t.Fatalf("probe = %v, want missing DialGuard to fail closed", seconds)
	}
}

func TestChainMediaJobProbeDefaultClosedForAdapter(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	chain := probeTestChain(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	record, ir, account := probeTestRecord(server.URL + "/artifact.mp4")
	account.ProviderCode = "openai"
	settlement := chain.chainMediaJobTerminalSettlement(context.Background(), account, record, ir)
	if !settlement.usageMissing {
		t.Fatalf("settlement = %+v, want usage_missing for adapter without probe", settlement)
	}
	if requests != 0 {
		t.Fatalf("adapter without probe reached server %d times", requests)
	}
}
