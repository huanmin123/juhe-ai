package mediaduration

import "testing"

func TestParseSegmentsMP4MoovLast(t *testing.T) {
	data := buildMP4(1000, 1500, false, 8<<20)
	total := int64(len(data))
	headEnd := int64(2 << 20)
	head := Segment{Offset: 0, Data: data[:headEnd]}
	tailStart := total - int64(4<<20)
	tail := Segment{Offset: tailStart, Data: data[tailStart:]}

	result, ok := ParseSegments([]Segment{head, tail}, total, Options{FormatHint: "mp4"})
	if !ok || result.Format != FormatMP4 || result.Seconds != 1.5 {
		t.Fatalf("ParseSegments mp4 moov-last = %+v/%v, want 1.5mp4", result, ok)
	}
}

func TestParseSegmentsMP4MoovFirst(t *testing.T) {
	data := buildMP4(1000, 1500, true, 8<<20)
	total := int64(len(data))
	head := Segment{Offset: 0, Data: data[:2<<20]}
	tailStart := total - int64(4<<20)
	tail := Segment{Offset: tailStart, Data: data[tailStart:]}

	result, ok := ParseSegments([]Segment{head, tail}, total, Options{FormatHint: "mp4"})
	if !ok || result.Format != FormatMP4 || result.Seconds != 1.5 {
		t.Fatalf("ParseSegments mp4 moov-first = %+v/%v, want 1.5mp4", result, ok)
	}
}

func TestParseSegmentsWebMInfo(t *testing.T) {
	webm := buildWebM(floatPtr(2.0), 1500, 500, true)
	total := int64(10 << 20)
	head := Segment{Offset: 0, Data: webm}
	tail := Segment{Offset: total - 4096, Data: make([]byte, 4096)}

	result, ok := ParseSegments([]Segment{head, tail}, total, Options{FormatHint: "webm"})
	if !ok || result.Format != FormatWebM || result.Seconds != 2.0 {
		t.Fatalf("ParseSegments webm info = %+v/%v, want 2webm", result, ok)
	}
}

func TestParseSegmentsWebMClusterFallback(t *testing.T) {
	webm := buildWebM(nil, 1500, 500, true)
	total := int64(10 << 20)
	head := Segment{Offset: 0, Data: webm[:20]}
	tail := Segment{Offset: total - int64(len(webm)), Data: webm}

	result, ok := ParseSegments([]Segment{head, tail}, total, Options{ContentType: "video/webm"})
	if !ok || result.Format != FormatWebM || result.Seconds != 2.0 {
		t.Fatalf("ParseSegments webm cluster = %+v/%v, want 2webm", result, ok)
	}
}

func TestParseSegmentsCompleteCoverageFallsBackToParse(t *testing.T) {
	data := buildWAV(48000, 1, 16, make([]byte, 48000*2))
	total := int64(len(data))
	head := Segment{Offset: 0, Data: data[:len(data)/2]}
	tail := Segment{Offset: int64(len(data) / 2), Data: data[len(data)/2:]}

	result, ok := ParseSegments([]Segment{head, tail}, total, Options{FormatHint: "wav"})
	if !ok || result.Format != FormatWAV || result.Seconds != 1.0 {
		t.Fatalf("ParseSegments complete wav = %+v/%v, want 1wav", result, ok)
	}
}

func TestParseSegmentsIncompleteNonContainerFails(t *testing.T) {
	data := buildWAV(48000, 1, 16, make([]byte, 48000*2))
	total := int64(len(data)) + 8<<20
	head := Segment{Offset: 0, Data: data[:len(data)/2]}

	if result, ok := ParseSegments([]Segment{head}, total, Options{FormatHint: "wav"}); ok {
		t.Fatalf("ParseSegments incomplete wav = %+v, want failure", result)
	}
}

func TestParseSegmentsRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		segments []Segment
		total    int64
	}{
		{name: "no-segments", total: 100},
		{name: "zero-total", segments: []Segment{{Data: []byte("x")}}},
		{name: "negative-offset", total: 100, segments: []Segment{{Offset: -1, Data: []byte("x")}}},
		{name: "offset-past-end", total: 100, segments: []Segment{{Offset: 100, Data: []byte("x")}}},
		{name: "empty-data", total: 100, segments: []Segment{{Offset: 0}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if result, ok := ParseSegments(testCase.segments, testCase.total, Options{FormatHint: "mp4"}); ok {
				t.Fatalf("ParseSegments = %+v, want failure", result)
			}
		})
	}
}

func floatPtr(value float64) *float64 { return &value }
