package queue

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"juhe-ai/backend-go/internal/platform/postgres"
)

const (
	defaultRedisDialTimeout  = 3 * time.Second
	defaultRedisReadTimeout  = 3 * time.Second
	defaultRedisWriteTimeout = 3 * time.Second
)

type RedisOptions struct {
	Addr         string
	Username     string
	Password     string
	DB           int
	TLS          *tls.Config
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

type Client struct {
	client *asynq.Client
}

type Inspector struct {
	inspector *asynq.Inspector
}

type EnqueueOptions struct {
	Queue     string
	MaxRetry  *int
	Timeout   time.Duration
	Retention time.Duration
	TaskID    string
	UniqueTTL time.Duration
}

var ErrTaskConflict = errors.New("queue task conflict")

type TaskInfo struct {
	ID       string
	Queue    string
	Type     string
	MaxRetry int
	State    string
}

type QueueInfo struct {
	Queue     string
	Size      int
	Pending   int
	Active    int
	Retry     int
	Archived  int
	Completed int
}

func NewClient(opts RedisOptions) *Client {
	return &Client{client: asynq.NewClient(opts.asynqOpt())}
}

func NewInspector(opts RedisOptions) *Inspector {
	return &Inspector{inspector: asynq.NewInspector(opts.asynqOpt())}
}

func (opts RedisOptions) asynqOpt() asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:         opts.Addr,
		Username:     opts.Username,
		Password:     opts.Password,
		DB:           opts.DB,
		TLSConfig:    opts.TLS,
		DialTimeout:  defaultDuration(opts.DialTimeout, defaultRedisDialTimeout),
		ReadTimeout:  defaultDuration(opts.ReadTimeout, defaultRedisReadTimeout),
		WriteTimeout: defaultDuration(opts.WriteTimeout, defaultRedisWriteTimeout),
	}
}

func (c *Client) Ping() error {
	return c.client.Ping()
}

func (c *Client) Enqueue(ctx context.Context, taskType string, payload []byte, opts EnqueueOptions) (TaskInfo, error) {
	if taskType == "" {
		return TaskInfo{}, fmt.Errorf("task type is required")
	}

	info, err := c.client.EnqueueContext(ctx, asynq.NewTask(taskType, payload), asynqOptions(opts)...)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return TaskInfo{}, fmt.Errorf("%w: %v", ErrTaskConflict, err)
		}
		return TaskInfo{}, err
	}

	return mapTaskInfo(info), nil
}

func asynqOptions(opts EnqueueOptions) []asynq.Option {
	taskOptions := make([]asynq.Option, 0, 6)
	if opts.Queue != "" {
		taskOptions = append(taskOptions, asynq.Queue(opts.Queue))
	}
	if opts.MaxRetry != nil {
		taskOptions = append(taskOptions, asynq.MaxRetry(*opts.MaxRetry))
	}
	if opts.Timeout > 0 {
		taskOptions = append(taskOptions, asynq.Timeout(opts.Timeout))
	}
	if opts.Retention > 0 {
		taskOptions = append(taskOptions, asynq.Retention(opts.Retention))
	}
	if opts.TaskID != "" {
		taskOptions = append(taskOptions, asynq.TaskID(opts.TaskID))
	}
	if opts.UniqueTTL > 0 {
		taskOptions = append(taskOptions, asynq.Unique(opts.UniqueTTL))
	}
	return taskOptions
}

func mapTaskInfo(info *asynq.TaskInfo) TaskInfo {
	return TaskInfo{
		ID:       info.ID,
		Queue:    info.Queue,
		Type:     info.Type,
		MaxRetry: info.MaxRetry,
		State:    info.State.String(),
	}
}

func (c *Client) Close() error {
	return c.client.Close()
}

func (i *Inspector) Close() error {
	return i.inspector.Close()
}

func (i *Inspector) QueueInfo(queue string) (QueueInfo, error) {
	info, err := i.inspector.GetQueueInfo(queue)
	if err != nil {
		return QueueInfo{}, err
	}
	return QueueInfo{
		Queue:     info.Queue,
		Size:      info.Size,
		Pending:   info.Pending,
		Active:    info.Active,
		Retry:     info.Retry,
		Archived:  info.Archived,
		Completed: info.Completed,
	}, nil
}

func (i *Inspector) PendingTaskIDs(queue string) ([]string, error) {
	tasks, err := i.inspector.ListPendingTasks(queue)
	if err != nil {
		return nil, err
	}
	return taskIDs(tasks), nil
}

func (i *Inspector) ArchivedTaskIDs(queue string) ([]string, error) {
	tasks, err := i.inspector.ListArchivedTasks(queue)
	if err != nil {
		return nil, err
	}
	return taskIDs(tasks), nil
}

func (i *Inspector) DeleteTask(queue string, id string) error {
	return i.inspector.DeleteTask(queue, id)
}

func ParseRedisURL(rawURL string) (RedisOptions, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return RedisOptions{}, err
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return RedisOptions{}, fmt.Errorf("unsupported redis scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return RedisOptions{}, fmt.Errorf("redis host is required")
	}

	password, _ := parsed.User.Password()
	opts := RedisOptions{
		Addr:         parsed.Host,
		Username:     parsed.User.Username(),
		Password:     password,
		DialTimeout:  defaultRedisDialTimeout,
		ReadTimeout:  defaultRedisReadTimeout,
		WriteTimeout: defaultRedisWriteTimeout,
	}

	path := strings.Trim(parsed.Path, "/")
	if path != "" {
		db, err := strconv.Atoi(path)
		if err != nil {
			return RedisOptions{}, fmt.Errorf("invalid redis db: %w", err)
		}
		opts.DB = db
	}

	port := parsed.Port()
	if port == "" {
		port = "6379"
	}
	opts.Addr = net.JoinHostPort(parsed.Hostname(), port)
	if parsed.Scheme == "rediss" {
		opts.TLS = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: parsed.Hostname(),
		}
	}

	return opts, nil
}

func Check(ctx context.Context, rawURL string) postgres.CheckResult {
	if rawURL == "" {
		return postgres.CheckResult{Configured: false, Status: "skipped"}
	}

	opts, err := ParseRedisURL(rawURL)
	if err != nil {
		return postgres.CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}

	client := NewClient(opts)
	defer func() {
		_ = client.Close()
	}()
	if err := client.Ping(); err != nil {
		return postgres.CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}

	return postgres.CheckResult{Configured: true, Status: "ok"}
}

func Smoke(ctx context.Context, rawURL string) error {
	opts, err := ParseRedisURL(rawURL)
	if err != nil {
		return err
	}

	client := NewClient(opts)
	defer func() {
		_ = client.Close()
	}()
	inspector := NewInspector(opts)
	defer func() {
		_ = inspector.Close()
	}()

	noRetry := 0
	info, err := client.Enqueue(ctx, "w0:smoke", []byte(`{"version":1}`), EnqueueOptions{
		Queue:     "w0-smoke",
		MaxRetry:  &noRetry,
		Timeout:   time.Second,
		Retention: time.Minute,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = inspector.DeleteTask("w0-smoke", info.ID)
	}()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		ids, err := inspector.PendingTaskIDs("w0-smoke")
		if err != nil {
			return err
		}
		for _, id := range ids {
			if id == info.ID {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func taskIDs(tasks []*asynq.TaskInfo) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func defaultDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
