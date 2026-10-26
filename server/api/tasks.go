package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"mapsim/platform"
)

// 短任务(AI 识图、色带识别)：提交后立即返回任务号，后台执行，完成后经 WebSocket 推送，也可轮询
// GET /v1/tasks/{id}。这样不受反向代理(Cloudflare 100 秒)的请求超时限制。
// 加 ?wait=1 时仍按同步方式返回结果(兼容脚本)。任务只在内存保留 1 小时，服务重启后丢失。

type Task struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"` // analyze | colorbands
	Status    string          `json:"status"`
	Created   string          `json:"created"`
	Finished  string          `json:"finished,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	ErrorCode string          `json:"errorCode,omitempty"`
	tenant    string
	done      chan struct{}
}

type taskError struct {
	code   string
	status int
	error
}

type taskStore struct {
	mu sync.Mutex
	m  map[string]*Task
}

func newTaskStore() *taskStore { return &taskStore{m: map[string]*Task{}} }

const taskTimeout = 5 * time.Minute

// start：每个用户同时只能有 1 个短任务在执行。
func (s *Server) startTask(tenant, kind string, fn func(ctx context.Context) (any, error)) (*Task, bool) {
	ts := s.tasks
	ts.mu.Lock()
	cut := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for id, t := range ts.m {
		if t.Status != "running" && t.Finished < cut {
			delete(ts.m, id)
		}
		if t.tenant == tenant && t.Status == "running" {
			ts.mu.Unlock()
			return nil, false
		}
	}
	t := &Task{ID: platform.NewID("task_"), Kind: kind, Status: "running", Created: time.Now().UTC().Format(time.RFC3339),
		tenant: tenant, done: make(chan struct{})}
	ts.m[t.ID] = t
	ts.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), taskTimeout)
		defer cancel()
		res, err := fn(ctx)
		ts.mu.Lock()
		t.Finished = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			t.Status, t.Error, t.ErrorCode = "failed", err.Error(), "task_failed"
			if te, ok := err.(*taskError); ok {
				t.ErrorCode = te.code
			}
		} else {
			t.Status = "succeeded"
			if raw, ok := res.(json.RawMessage); ok {
				t.Result = raw
			} else {
				t.Result, _ = json.Marshal(res)
			}
		}
		snap := *t
		close(t.done)
		ts.mu.Unlock()
		s.broadcast(tenant, map[string]any{"type": "task", "task": snap})
	}()
	return t, true
}

func (s *Server) getTask(id, tenant string) (*Task, bool) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	t, ok := s.tasks.m[id]
	if !ok || t.tenant != tenant {
		return nil, false
	}
	c := *t
	return &c, true
}

func (s *Server) taskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tasks/{id}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		t, ok := s.getTask(r.PathValue("id"), tenantOf(r).ID)
		if !ok {
			fail(w, 404, "not_found", "任务不存在")
			return
		}
		writeJSON(w, 200, t)
	}))
}

// respondTask：默认返回 202 + 任务；?wait=1 时等任务结束，按原同步接口的格式返回结果或错误。
func (s *Server) respondTask(w http.ResponseWriter, r *http.Request, t *Task) {
	if r.URL.Query().Get("wait") == "" {
		writeJSON(w, 202, t)
		return
	}
	select {
	case <-t.done:
	case <-r.Context().Done():
		return
	}
	f, _ := s.getTask(t.ID, t.tenant)
	if f.Status == "succeeded" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(f.Result)
		return
	}
	status := 502
	if f.ErrorCode == "colorband_failed" {
		status = 422
	}
	fail(w, status, f.ErrorCode, f.Error)
}

func (s *Server) taskBusy(w http.ResponseWriter) {
	fail(w, 429, "task_busy", "上一个识别任务还在进行中，请等它完成")
}
