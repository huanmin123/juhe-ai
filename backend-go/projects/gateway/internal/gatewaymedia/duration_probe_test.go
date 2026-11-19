package gatewaymedia

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func probeMP4Box(boxType string, payload []byte) []byte {
	box := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(box[0:4], uint32(8+len(payload)))
	copy(box[4:8], boxType)
	return append(box, payload...)
}

func probeMP4(timescale, duration uint32, mdatSize int, moovFirst bool) []byte {
	ftyp := probeMP4Box("ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
	mvhdPayload := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], timescale)
	binary.BigEndian.PutUint32(mvhdPayload[16:20], duration)
	moov := probeMP4Box("moov", probeMP4Box("mvhd", mvhdPayload))
	mdat := probeMP4Box("mdat", make([]byte, mdatSize))
	data := append([]byte{}, ftyp...)
	if moovFirst {
		data = append(data, moov...)
		data = append(data, mdat...)
	} else {
		data = append(data, mdat...)
		data = append(data, moov...)
	}
	return data
}

func TestProbeArtifactDurationLargePartial(t *testing.T) {
	data := probeMP4(1000, 1500, 8<<20, false)
	total := int64(len(data))
	tailStart := total - ArtifactProbeTailBytes
	calls := 0
	fetch := func(_ context.Context, rawURL string, start, end int64) (int, http.Header, []byte, error) {
		if rawURL != "https://cdn.example.com/artifact.mp4" {
			t.Fatalf("rawURL = %q", rawURL)
		}
		calls++
		switch calls {
		case 1:
			if start != 0 || end != ArtifactProbeHeadBytes-1 {
				t.Fatalf("head range = %d-%d", start, end)
			}
			return http.StatusPartialContent, http.Header{
				"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", ArtifactProbeHeadBytes-1, total)},
				"Content-Type":  {"video/mp4"},
			}, data[:ArtifactProbeHeadBytes], nil
		case 2:
			if start != tailStart || end != total-1 {
				t.Fatalf("tail range = %d-%d, want %d-%d", start, end, tailStart, total-1)
			}
			return http.StatusPartialContent, http.Header{
				"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", tailStart, total-1, total)},
				"Content-Type":  {"video/mp4"},
			}, data[tailStart:], nil
		default:
			t.Fatalf("unexpected fetch call %d", calls)
			return 0, nil, nil, nil
		}
	}

	seconds, err := ProbeArtifactDuration(context.Background(), "https://cdn.example.com/artifact.mp4", fetch)
	if err != nil || seconds != 1.5 {
		t.Fatalf("ProbeArtifactDuration = %v/%v, want 1.5", seconds, err)
	}
}

func TestProbeArtifactDurationSmallObjectWithoutRange(t *testing.T) {
	data := probeMP4(1000, 2000, 1024, true)
	fetch := func(_ context.Context, _ string, start, end int64) (int, http.Header, []byte, error) {
		if start != 0 || end != ArtifactProbeHeadBytes-1 {
			t.Fatalf("range = %d-%d", start, end)
		}
		return http.StatusOK, http.Header{"Content-Type": {"video/mp4"}}, data, nil
	}

	seconds, err := ProbeArtifactDuration(context.Background(), "https://cdn.example.com/artifact.mp4", fetch)
	if err != nil || seconds != 2.0 {
		t.Fatalf("ProbeArtifactDuration = %v/%v, want 2", seconds, err)
	}
}

func TestProbeArtifactDurationSmallObjectPartial(t *testing.T) {
	data := probeMP4(1000, 2000, 1024, true)
	fetch := func(_ context.Context, _ string, start, end int64) (int, http.Header, []byte, error) {
		if start != 0 || end != ArtifactProbeHeadBytes-1 {
			t.Fatalf("range = %d-%d", start, end)
		}
		return http.StatusPartialContent, http.Header{
			"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data))},
			"Content-Type":  {"video/mp4"},
		}, data, nil
	}

	seconds, err := ProbeArtifactDuration(context.Background(), "https://cdn.example.com/artifact.mp4", fetch)
	if err != nil || seconds != 2.0 {
		t.Fatalf("ProbeArtifactDuration = %v/%v, want 2", seconds, err)
	}
}

func TestProbeArtifactDurationRejectsRangeIgnoredLargeBody(t *testing.T) {
	fetch := func(_ context.Context, _ string, _, _ int64) (int, http.Header, []byte, error) {
		return http.StatusOK, http.Header{"Content-Type": {"video/mp4"}}, make([]byte, ArtifactProbeHeadBytes+1), nil
	}

	_, err := ProbeArtifactDuration(context.Background(), "https://cdn.example.com/artifact.mp4", fetch)
	if !errors.Is(err, ErrArtifactProbeUnsupported) {
		t.Fatalf("err = %v, want ErrArtifactProbeUnsupported", err)
	}
}

func TestProbeArtifactDurationRejectsBadContentRange(t *testing.T) {
	fetch := func(_ context.Context, _ string, _, _ int64) (int, http.Header, []byte, error) {
		return http.StatusPartialContent, http.Header{"Content-Range": {"bytes */123"}}, []byte("x"), nil
	}

	_, err := ProbeArtifactDuration(context.Background(), "https://cdn.example.com/artifact.mp4", fetch)
	if !errors.Is(err, ErrArtifactProbeUnsupported) {
		t.Fatalf("err = %v, want ErrArtifactProbeUnsupported", err)
	}
}

func TestProbeArtifactDurationRejectsUnparsableBody(t *testing.T) {
	fetch := func(_ context.Context, _ string, _, _ int64) (int, http.Header, []byte, error) {
		return http.StatusOK, http.Header{"Content-Type": {"video/mp4"}}, []byte("not a video"), nil
	}

	_, err := ProbeArtifactDuration(context.Background(), "https://cdn.example.com/artifact.mp4", fetch)
	if !errors.Is(err, ErrArtifactProbeNoDuration) {
		t.Fatalf("err = %v, want ErrArtifactProbeNoDuration", err)
	}
}

func TestProbeArtifactDurationRejectsUserinfoAndNonHTTP(t *testing.T) {
	fetch := func(_ context.Context, _ string, _, _ int64) (int, http.Header, []byte, error) {
		t.Fatal("invalid URL must not reach fetch")
		return 0, nil, nil, nil
	}
	for _, rawURL := range []string{
		"https://user:pass@cdn.example.com/artifact.mp4",
		"file:///tmp/artifact.mp4",
		"ftp://cdn.example.com/artifact.mp4",
		"https:///artifact.mp4",
	} {
		if _, err := ProbeArtifactDuration(context.Background(), rawURL, fetch); !errors.Is(err, ErrArtifactProbeInvalidURL) {
			t.Fatalf("ProbeArtifactDuration(%q) err = %v, want ErrArtifactProbeInvalidURL", rawURL, err)
		}
	}
}

func TestProbeArtifactDurationHonorsParentTimeout(t *testing.T) {
	fetch := func(ctx context.Context, _ string, _, _ int64) (int, http.Header, []byte, error) {
		<-ctx.Done()
		return 0, nil, nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := ProbeArtifactDuration(ctx, "https://cdn.example.com/artifact.mp4", fetch)
	if !errors.Is(err, ErrArtifactProbeFetch) {
		t.Fatalf("err = %v, want ErrArtifactProbeFetch", err)
	}
	if strings.Contains(err.Error(), "cdn.example.com") {
		t.Fatalf("error leaks artifact URL: %v", err)
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		value string
		ok    bool
		start int64
		end   int64
		total int64
	}{
		{value: "bytes 0-2097151/12345678", ok: true, start: 0, end: 2097151, total: 12345678},
		{value: "Bytes 10-20/21", ok: true, start: 10, end: 20, total: 21},
		{value: "bytes */123", ok: false},
		{value: "bytes 1-0/10", ok: false},
		{value: "bytes 0-9/10 ", ok: true, start: 0, end: 9, total: 10},
		{value: "", ok: false},
	}
	for _, testCase := range cases {
		start, end, total, ok := parseContentRange(testCase.value)
		if ok != testCase.ok || start != testCase.start || end != testCase.end || total != testCase.total {
			t.Fatalf("parseContentRange(%q) = %d,%d,%d,%v; want %d,%d,%d,%v", testCase.value, start, end, total, ok, testCase.start, testCase.end, testCase.total, testCase.ok)
		}
	}
}

func TestArtifactFormatHint(t *testing.T) {
	parsed := func(raw string) *url.URL {
		value, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for raw, want := range map[string]string{
		"https://cdn.example.com/a.MP4?signature=x": "mp4",
		"https://cdn.example.com/a.mov":             "mp4",
		"https://cdn.example.com/a.webm":            "webm",
		"https://cdn.example.com/a.m3u8":            "",
	} {
		if got := artifactFormatHint(parsed(raw)); got != want {
			t.Fatalf("artifactFormatHint(%q) = %q, want %q", raw, got, want)
		}
	}
}
