package modelcheckauth

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"
	"time"
)

const (
	captchaTTL           = 5 * time.Minute
	captchaIssueWindow   = time.Minute
	captchaIssueLimit    = 60
	captchaMaxChallenges = 1000
)

type CaptchaChallenge struct {
	CaptchaID string
	Image     string
	ExpiresAt time.Time
}

type CaptchaIssueResult struct {
	Challenge  CaptchaChallenge
	Blocked    bool
	RetryAfter int
	Message    string
}

type captchaRecord struct {
	answer    string
	expiresAt time.Time
}

// CaptchaService mirrors the Node challenge contract. It is process-local;
// production multi-instance use requires a shared runtime-state adapter.
type CaptchaService struct {
	mu     sync.Mutex
	now    func() time.Time
	chall  map[string]captchaRecord
	issues map[string][]time.Time
}

func NewCaptchaService(now func() time.Time) *CaptchaService {
	if now == nil {
		now = time.Now
	}
	return &CaptchaService{now: now, chall: make(map[string]captchaRecord), issues: make(map[string][]time.Time)}
}

func (s *CaptchaService) Issue(clientIP string) (CaptchaIssueResult, error) {
	if s == nil {
		return CaptchaIssueResult{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	ip := strings.TrimSpace(clientIP)
	recent := recentTimestamps(s.issues[ip], now)
	if len(recent) >= captchaIssueLimit {
		retry := int((recent[0].Add(captchaIssueWindow).Sub(now) + time.Second - 1) / time.Second)
		if retry < 1 {
			retry = 1
		}
		s.issues[ip] = recent
		return CaptchaIssueResult{Blocked: true, RetryAfter: retry, Message: "验证码请求过于频繁，请稍后再试"}, nil
	}
	recent = append(recent, now)
	s.issues[ip] = recent
	for id, record := range s.chall {
		if !record.expiresAt.After(now) {
			delete(s.chall, id)
		}
	}
	for len(s.chall) >= captchaMaxChallenges {
		for id := range s.chall {
			delete(s.chall, id)
			break
		}
	}
	answer, err := randomCaptchaAnswer()
	if err != nil {
		return CaptchaIssueResult{}, err
	}
	id, err := randomCaptchaID()
	if err != nil {
		return CaptchaIssueResult{}, err
	}
	expiresAt := now.Add(captchaTTL)
	s.chall[id] = captchaRecord{answer: answer, expiresAt: expiresAt}
	return CaptchaIssueResult{Challenge: CaptchaChallenge{CaptchaID: id, Image: renderCaptchaImage(answer), ExpiresAt: expiresAt}}, nil
}

func (s *CaptchaService) Verify(captchaID, code string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.chall[captchaID]
	delete(s.chall, captchaID)
	if !ok || !record.expiresAt.After(s.now().UTC()) {
		return false
	}
	return normalizeCaptchaCode(code) == record.answer
}

// AnswerForTest is intentionally not used by HTTP handlers.
func (s *CaptchaService) AnswerForTest(captchaID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.chall[captchaID]
	if !ok || !record.expiresAt.After(s.now().UTC()) {
		return ""
	}
	return record.answer
}

func randomCaptchaAnswer() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	var answer [5]byte
	var random [5]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	for i, value := range random {
		answer[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(answer[:]), nil
}

func randomCaptchaID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func normalizeCaptchaCode(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(value), ""))
}

func recentTimestamps(values []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-captchaIssueWindow)
	result := values[:0]
	for _, value := range values {
		if !value.Before(cutoff) {
			result = append(result, value)
		}
	}
	return result
}

func renderCaptchaImage(answer string) string {
	const (
		width  = 108
		height = 52
		scale  = 3
	)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 247, G: 249, B: 252, A: 255}}, image.Point{}, draw.Src)
	for i := 0; i < 10; i++ {
		x := (i*17 + 11) % width
		for y := 0; y < height; y++ {
			if (x+y+i)%13 == 0 {
				img.SetRGBA(x, y, color.RGBA{R: 185, G: 195, B: 208, A: 255})
			}
		}
	}
	startX := (width - (len(answer)*5*scale + (len(answer)-1)*scale)) / 2
	for index, char := range answer {
		glyph, ok := captchaGlyphs[char]
		if !ok {
			continue
		}
		originX := startX + index*(6*scale)
		for row, pattern := range glyph {
			for column, pixel := range pattern {
				if pixel != '1' {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.SetRGBA(originX+column*scale+dx, 12+row*scale+dy, color.RGBA{R: 34, G: 40, B: 43, A: 255})
					}
				}
			}
		}
	}
	var encoded bytes.Buffer
	_ = png.Encode(&encoded, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
}

var captchaGlyphs = map[rune][]string{
	'2': {"11110", "00001", "00001", "11110", "10000", "10000", "11111"}, '3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"10001", "10001", "10001", "11111", "00001", "00001", "00001"}, '5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"01111", "10000", "10000", "11110", "10001", "10001", "01110"}, '7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"}, '9': {"01110", "10001", "10001", "01111", "00001", "00001", "11110"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"}, 'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01111", "10000", "10000", "10000", "10000", "10000", "01111"}, 'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"}, 'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01111", "10000", "10000", "10011", "10001", "10001", "01111"}, 'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'J': {"00111", "00010", "00010", "00010", "10010", "10010", "01100"}, 'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"}, 'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"}, 'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"}, 'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"}, 'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"}, 'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "10101", "01010"}, 'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"}, 'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}
