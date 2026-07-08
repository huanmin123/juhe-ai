package managementauth

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

const (
	CaptchaTTL            = 5 * time.Minute
	CaptchaIssueWindow    = time.Minute
	CaptchaIssueThreshold = 60

	captchaAnswerLength = 5
	captchaChars        = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
)

var ErrCaptchaStoreRequired = errors.New("management auth captcha store is required")

type CaptchaStateStore interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	GetDelete(ctx context.Context, key string) ([]byte, error)
	AllowFixedWindow(ctx context.Context, limits []redisplatform.FixedWindowLimit) (redisplatform.FixedWindowDecision, error)
}

type CaptchaChallenge struct {
	CaptchaID string `json:"captchaId"`
	Image     string `json:"image"`
	ExpiresAt string `json:"expiresAt"`
}

type CaptchaIssueLimitError struct {
	RetryAfterSeconds int
}

func (e *CaptchaIssueLimitError) Error() string {
	return "captcha issue rate limited"
}

type CaptchaService struct {
	store          CaptchaStateStore
	now            func() time.Time
	newID          func() string
	generateAnswer func() (string, error)
	renderImage    func(string) (string, error)
}

type CaptchaServiceOptions struct {
	Store          CaptchaStateStore
	Now            func() time.Time
	NewID          func() string
	GenerateAnswer func() (string, error)
	RenderImage    func(string) (string, error)
}

type captchaChallengeRecord struct {
	Answer    string `json:"answer"`
	ExpiresAt int64  `json:"expiresAt"`
}

func NewCaptchaService(store CaptchaStateStore) *CaptchaService {
	return NewCaptchaServiceWithOptions(CaptchaServiceOptions{Store: store})
}

func NewCaptchaServiceWithOptions(opts CaptchaServiceOptions) *CaptchaService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	generateAnswer := opts.GenerateAnswer
	if generateAnswer == nil {
		generateAnswer = GenerateCaptchaAnswer
	}
	renderImage := opts.RenderImage
	if renderImage == nil {
		renderImage = RenderCaptchaImage
	}
	return &CaptchaService{
		store:          opts.Store,
		now:            now,
		newID:          newID,
		generateAnswer: generateAnswer,
		renderImage:    renderImage,
	}
}

func (s *CaptchaService) IssueChallenge(ctx context.Context, clientIP string) (CaptchaChallenge, error) {
	if s == nil || s.store == nil {
		return CaptchaChallenge{}, ErrCaptchaStoreRequired
	}
	decision, err := s.store.AllowFixedWindow(ctx, []redisplatform.FixedWindowLimit{
		{Key: CaptchaIssueRateLimitKey(clientIP), Limit: CaptchaIssueThreshold, Window: CaptchaIssueWindow},
	})
	if err != nil {
		return CaptchaChallenge{}, err
	}
	if !decision.Allowed {
		retryAfter := decision.RetryAfterSeconds
		if retryAfter <= 0 {
			retryAfter = int(CaptchaIssueWindow / time.Second)
		}
		return CaptchaChallenge{}, &CaptchaIssueLimitError{RetryAfterSeconds: retryAfter}
	}

	answer, err := s.generateAnswer()
	if err != nil {
		return CaptchaChallenge{}, err
	}
	answer = NormalizeCaptchaCode(answer)
	if len(answer) != captchaAnswerLength {
		return CaptchaChallenge{}, fmt.Errorf("captcha answer length = %d, want %d", len(answer), captchaAnswerLength)
	}

	captchaID := s.newID()
	expiresAt := s.now().UTC().Add(CaptchaTTL)
	record, err := json.Marshal(captchaChallengeRecord{
		Answer:    answer,
		ExpiresAt: expiresAt.UnixMilli(),
	})
	if err != nil {
		return CaptchaChallenge{}, err
	}
	imageDataURL, err := s.renderImage(answer)
	if err != nil {
		return CaptchaChallenge{}, err
	}
	if err := s.store.Set(ctx, CaptchaChallengeKey(captchaID), record, CaptchaTTL); err != nil {
		return CaptchaChallenge{}, err
	}

	return CaptchaChallenge{
		CaptchaID: captchaID,
		Image:     imageDataURL,
		ExpiresAt: formatNodeISOString(expiresAt),
	}, nil
}

func (s *CaptchaService) VerifyChallenge(ctx context.Context, captchaID string, captchaCode string) (bool, error) {
	if s == nil || s.store == nil {
		return false, ErrCaptchaStoreRequired
	}
	value, err := s.store.GetDelete(ctx, CaptchaChallengeKey(captchaID))
	if errors.Is(err, redisplatform.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var record captchaChallengeRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return false, err
	}
	if record.ExpiresAt <= s.now().UTC().UnixMilli() {
		return false, nil
	}
	return NormalizeCaptchaCode(captchaCode) == record.Answer, nil
}

func GenerateCaptchaAnswer() (string, error) {
	var builder strings.Builder
	builder.Grow(captchaAnswerLength)
	for i := 0; i < captchaAnswerLength; i++ {
		index, err := cryptoRandomInt(len(captchaChars))
		if err != nil {
			return "", err
		}
		builder.WriteByte(captchaChars[index])
	}
	return builder.String(), nil
}

func NormalizeCaptchaCode(value string) string {
	var builder strings.Builder
	for _, item := range strings.TrimSpace(value) {
		if unicode.IsSpace(item) {
			continue
		}
		builder.WriteRune(unicode.ToUpper(item))
	}
	return builder.String()
}

func CaptchaChallengeKey(captchaID string) string {
	return "auth_captcha:challenge:" + strings.TrimSpace(captchaID)
}

func CaptchaIssueRateLimitKey(clientIP string) string {
	text := strings.TrimSpace(clientIP)
	if text == "" {
		text = "unknown"
	}
	sum := sha256.Sum256([]byte("auth_captcha_issue\x00" + text))
	return "auth_captcha:issue:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

func RenderCaptchaImage(answer string) (string, error) {
	const width = 144
	const height = 46
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{
				R: 239,
				G: uint8(246 + y*6/max(1, height-1)),
				B: uint8(242 + x*10/max(1, width-1)),
				A: 255,
			})
		}
	}

	for i := 0; i < 6; i++ {
		x1, err := cryptoRandomInt(width)
		if err != nil {
			return "", err
		}
		y1, err := cryptoRandomInt(height)
		if err != nil {
			return "", err
		}
		x2, err := cryptoRandomInt(width)
		if err != nil {
			return "", err
		}
		y2, err := cryptoRandomInt(height)
		if err != nil {
			return "", err
		}
		drawCaptchaLine(img, x1, y1, x2, y2, color.RGBA{R: 37, G: 99, B: 235, A: 255})
	}

	for i := 0; i < 28; i++ {
		x, err := cryptoRandomInt(width - 8)
		if err != nil {
			return "", err
		}
		y, err := cryptoRandomInt(height - 8)
		if err != nil {
			return "", err
		}
		rectWidth, err := cryptoRandomIntRange(1, 3)
		if err != nil {
			return "", err
		}
		rectHeight, err := cryptoRandomIntRange(1, 3)
		if err != nil {
			return "", err
		}
		drawCaptchaRect(img, 4+x, 4+y, rectWidth, rectHeight, color.RGBA{R: 14, G: 165, B: 233, A: 255})
	}

	for index, char := range answer {
		offsetX, err := cryptoRandomIntRange(-2, 3)
		if err != nil {
			return "", err
		}
		offsetY, err := cryptoRandomIntRange(-2, 3)
		if err != nil {
			return "", err
		}
		drawCaptchaGlyph(img, char, 13+index*24+offsetX, 8+offsetY, 4)
	}

	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(output.Bytes()), nil
}

func drawCaptchaGlyph(img *image.RGBA, char rune, startX int, startY int, scale int) {
	glyph := captchaGlyphs[char]
	for rowIndex, row := range glyph {
		for columnIndex, value := range row {
			if value != '1' {
				continue
			}
			blockX := startX + columnIndex*scale
			blockY := startY + rowIndex*scale
			drawCaptchaRect(img, blockX, blockY, scale, scale, color.RGBA{R: 25, G: 55, B: 95, A: 255})
		}
	}
}

func drawCaptchaRect(img *image.RGBA, startX int, startY int, width int, height int, value color.RGBA) {
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			px := startX + x
			py := startY + y
			if px < 0 || py < 0 || px >= img.Bounds().Dx() || py >= img.Bounds().Dy() {
				continue
			}
			img.SetRGBA(px, py, value)
		}
	}
}

func drawCaptchaLine(img *image.RGBA, x1 int, y1 int, x2 int, y2 int, value color.RGBA) {
	currentX := x1
	currentY := y1
	dx := absInt(x2 - x1)
	dy := -absInt(y2 - y1)
	stepX := -1
	if x1 < x2 {
		stepX = 1
	}
	stepY := -1
	if y1 < y2 {
		stepY = 1
	}
	err := dx + dy
	for {
		drawCaptchaRect(img, currentX, currentY, 1, 1, value)
		if currentX == x2 && currentY == y2 {
			break
		}
		doubled := 2 * err
		if doubled >= dy {
			err += dy
			currentX += stepX
		}
		if doubled <= dx {
			err += dx
			currentY += stepY
		}
	}
}

func cryptoRandomInt(maxValue int) (int, error) {
	if maxValue <= 0 {
		return 0, fmt.Errorf("random max must be positive")
	}
	value, err := crand.Int(crand.Reader, big.NewInt(int64(maxValue)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func cryptoRandomIntRange(minValue int, maxValue int) (int, error) {
	if maxValue <= minValue {
		return minValue, nil
	}
	value, err := cryptoRandomInt(maxValue - minValue)
	if err != nil {
		return 0, err
	}
	return minValue + value, nil
}

func formatNodeISOString(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

var captchaGlyphs = map[rune][]string{
	'2': {"11110", "00001", "00001", "11110", "10000", "10000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"10001", "10001", "10001", "11111", "00001", "00001", "00001"},
	'5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"01111", "10000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00001", "11110"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01111", "10000", "10000", "10000", "10000", "10000", "01111"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01111", "10000", "10000", "10011", "10001", "10001", "01111"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'J': {"00111", "00010", "00010", "00010", "10010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "10101", "01010"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}
