package authsys

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"time"

	"bytes"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// Shared captcha contract constants mirror captcha.service.ts:
// captchaTtlMs 5min, captchaIssueWindowMs 1min, captchaIssueThreshold 60.
const (
	sharedCaptchaTTL         = 5 * time.Minute
	sharedCaptchaIssueWindow = time.Minute
	sharedCaptchaIssueLimit  = int64(60)
)

// sharedCaptchaStateStoreName mirrors createRuntimeStateStore('auth_captcha')
// so Go and Node share one Redis key space during the migration window.
const sharedCaptchaStateStoreName = "auth_captcha"

// captchaChallengeRecord mirrors CaptchaChallengeRecord (answer + epoch-ms
// expiry) so Node-issued challenges verify in Go and vice versa.
type captchaChallengeRecord struct {
	Answer    string `json:"answer"`
	ExpiresAt int64  `json:"expiresAt"`
}

// SharedCaptchaService is the Redis runtime-state captcha driver: it mirrors
// the Node async paths (createCaptchaChallengeAsync /
// verifyCaptchaChallengeAsync / consumeCaptchaIssueAllowanceAsync) so challenge
// consumption is atomic across instances (GETDEL) and the per-IP issue window
// is a shared counter (BUG-0171.4). The memory driver remains
// modelcheckauth.CaptchaService. Verify surfaces state-store failures as a
// failed verification (the CaptchaIssuer surface is error-free because the
// memory driver must keep satisfying it); Issue propagates them to the
// existing 500 path.
type SharedCaptchaService struct {
	store RedisStateStore
	now   func() time.Time
}

// NewSharedCaptchaService builds the driver over an injected state store.
func NewSharedCaptchaService(store RedisStateStore, now func() time.Time) *SharedCaptchaService {
	if now == nil {
		now = time.Now
	}
	return &SharedCaptchaService{store: store, now: now}
}

// NewRedisCaptchaService builds the driver over the auth_captcha Redis state
// store; the returned close func releases the client.
func NewRedisCaptchaService(url, namespace string, now func() time.Time) (*SharedCaptchaService, func(), error) {
	store, closeFn, err := NewRedisNamespacedStateStore(url, namespace, sharedCaptchaStateStoreName)
	if err != nil {
		return nil, nil, err
	}
	return NewSharedCaptchaService(store, now), closeFn, nil
}

func (s *SharedCaptchaService) challengeKey(captchaID string) string {
	return "challenge:" + captchaID
}

func (s *SharedCaptchaService) issueKey(clientIP string) string {
	trimmed := strings.TrimSpace(clientIP)
	if trimmed == "" {
		trimmed = "unknown"
	}
	return "issue:" + trimmed
}

// Issue mirrors consumeCaptchaIssueAllowanceAsync + createCaptchaChallengeAsync:
// a shared incr counter enforces 60 issues per IP per minute, then the
// challenge is stored with a 5-minute TTL.
func (s *SharedCaptchaService) Issue(clientIP string) (modelcheckauth.CaptchaIssueResult, error) {
	now := s.now().UTC()
	count, err := s.store.Incr(nil, s.issueKey(clientIP), sharedCaptchaIssueWindow.Milliseconds(), sharedCaptchaIssueLimit)
	if err != nil {
		return modelcheckauth.CaptchaIssueResult{}, err
	}
	if count > sharedCaptchaIssueLimit {
		return modelcheckauth.CaptchaIssueResult{
			Blocked:    true,
			RetryAfter: int(sharedCaptchaIssueWindow / time.Second),
			Message:    "验证码请求过于频繁，请稍后再试",
		}, nil
	}
	answer, err := sharedCaptchaAnswer()
	if err != nil {
		return modelcheckauth.CaptchaIssueResult{}, err
	}
	id, err := sharedCaptchaID()
	if err != nil {
		return modelcheckauth.CaptchaIssueResult{}, err
	}
	expiresAt := now.Add(sharedCaptchaTTL)
	if err := s.store.SetJSON(nil, s.challengeKey(id), captchaChallengeRecord{Answer: answer, ExpiresAt: expiresAt.UnixMilli()}, sharedCaptchaTTL.Milliseconds()); err != nil {
		return modelcheckauth.CaptchaIssueResult{}, err
	}
	return modelcheckauth.CaptchaIssueResult{
		Challenge: modelcheckauth.CaptchaChallenge{
			CaptchaID: id,
			Image:     renderSharedCaptchaImage(answer),
			ExpiresAt: expiresAt,
		},
	}, nil
}

// Verify mirrors verifyCaptchaChallengeAsync: GETDEL consumes the challenge
// exactly once, then the expiry and normalized answer decide the result.
func (s *SharedCaptchaService) Verify(captchaID, code string) bool {
	var record captchaChallengeRecord
	ok, err := s.store.GetDeleteJSON(nil, s.challengeKey(captchaID), &record)
	if err != nil || !ok {
		return false
	}
	if record.ExpiresAt < s.now().UnixMilli() {
		return false
	}
	return normalizeSharedCaptchaCode(code) == record.Answer
}

const sharedCaptchaAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

func sharedCaptchaAnswer() (string, error) {
	var random [5]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	answer := make([]byte, len(random))
	for i, value := range random {
		answer[i] = sharedCaptchaAlphabet[int(value)%len(sharedCaptchaAlphabet)]
	}
	return string(answer), nil
}

func sharedCaptchaID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// normalizeSharedCaptchaCode mirrors normalizeCaptchaCode.
func normalizeSharedCaptchaCode(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(value), ""))
}

// renderSharedCaptchaImage mirrors the modelcheckauth memory-driver renderer
// (same glyph raster): the renderer there is unexported and this package
// cannot modify modelcheckauth, so the raster is replicated to keep
// Go-issued challenge images visually identical across drivers.
func renderSharedCaptchaImage(answer string) string {
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
		glyph, ok := sharedCaptchaGlyphs[char]
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

var sharedCaptchaGlyphs = map[rune][]string{
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

var _ CaptchaIssuer = (*SharedCaptchaService)(nil)
