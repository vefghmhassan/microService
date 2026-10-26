// Package jobs runs long operations (provision, rotate, sync, …) in the
// background, persisting state and logs so the UI can follow them live and a
// restart resumes them instead of losing them.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"gorm.io/gorm"

	"microserver/internal/models"
)

type Handler func(ctx context.Context, r *Run) error

type Engine struct {
	db       *gorm.DB
	Broker   *Broker
	handlers map[string]Handler
	queue    chan uint
	workers  int

	mu       sync.Mutex
	locks    map[uint]*sync.Mutex
	cancels  map[uint]context.CancelFunc
	onFinish []func(*models.Job)
}

func NewEngine(db *gorm.DB, workers int) *Engine {
	return &Engine{
		db: db, Broker: NewBroker(), handlers: map[string]Handler{},
		queue: make(chan uint, 1024), workers: workers,
		locks: map[uint]*sync.Mutex{}, cancels: map[uint]context.CancelFunc{},
	}
}

func (e *Engine) Register(kind string, h Handler) { e.handlers[kind] = h }

// OnFinish registers a callback for jobs that ended (done, failed, canceled).
func (e *Engine) OnFinish(f func(*models.Job)) { e.onFinish = append(e.onFinish, f) }

// Enqueue stores a job and schedules it.
func (e *Engine) Enqueue(kind string, serverID *uint, params any, source string) (*models.Job, error) {
	if _, ok := e.handlers[kind]; !ok {
		return nil, fmt.Errorf("unknown job kind %q", kind)
	}
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	j := &models.Job{Kind: kind, Status: models.JobQueued, ServerID: serverID, Params: string(p), State: "{}", Source: source}
	if err := e.db.Create(j).Error; err != nil {
		return nil, err
	}
	e.Broker.Publish(Event{Type: "job", JobID: j.ID, ServerID: serverID, Data: j})
	e.queue <- j.ID
	return j, nil
}

// Start resumes interrupted jobs and launches the workers.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.db.Model(&models.Job{}).Where("status = ?", models.JobRunning).
		Update("status", models.JobQueued).Error; err != nil {
		return err
	}
	var pending []models.Job
	if err := e.db.Where("status = ?", models.JobQueued).Order("id").Find(&pending).Error; err != nil {
		return err
	}
	for _, j := range pending {
		e.queue <- j.ID
	}
	for i := 0; i < e.workers; i++ {
		go e.worker(ctx)
	}
	return nil
}

func (e *Engine) Cancel(id uint) error {
	e.mu.Lock()
	cancel, running := e.cancels[id]
	e.mu.Unlock()
	if running {
		cancel()
		return nil
	}
	res := e.db.Model(&models.Job{}).Where("id = ? AND status = ?", id, models.JobQueued).
		Updates(map[string]any{"status": models.JobCanceled, "finished_at": time.Now()})
	if res.RowsAffected == 0 {
		return errors.New("job is not queued or running")
	}
	return nil
}

func (e *Engine) serverLock(id uint) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.locks[id]
	if !ok {
		l = &sync.Mutex{}
		e.locks[id] = l
	}
	return l
}

func (e *Engine) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-e.queue:
			e.run(ctx, id)
		}
	}
}

func (e *Engine) run(parent context.Context, id uint) {
	var j models.Job
	if err := e.db.First(&j, id).Error; err != nil || j.Status != models.JobQueued {
		return
	}
	// One job per server at a time: two rotations racing on one machine
	// would each think they own its IP.
	if j.ServerID != nil {
		l := e.serverLock(*j.ServerID)
		l.Lock()
		defer l.Unlock()
		if err := e.db.First(&j, id).Error; err != nil || j.Status != models.JobQueued {
			return
		}
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	e.mu.Lock()
	e.cancels[id] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.cancels, id)
		e.mu.Unlock()
	}()

	now := time.Now()
	j.Status = models.JobRunning
	if j.StartedAt == nil {
		j.StartedAt = &now
	}
	e.db.Model(&j).Updates(map[string]any{"status": j.Status, "started_at": j.StartedAt, "error": ""})
	e.Broker.Publish(Event{Type: "job", JobID: j.ID, ServerID: j.ServerID, Data: j})

	r := &Run{Job: &j, e: e, state: map[string]any{}}
	_ = json.Unmarshal([]byte(j.State), &r.state)

	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
				log.Printf("job %d panic: %v\n%s", id, p, debug.Stack())
			}
		}()
		return e.handlers[j.Kind](ctx, r)
	}()

	end := time.Now()
	j.FinishedAt = &end
	switch {
	case err == nil:
		j.Status = models.JobDone
		r.Logf("info", "done")
	case errors.Is(ctx.Err(), context.Canceled) && parent.Err() == nil:
		j.Status = models.JobCanceled
		j.Error = "canceled"
		r.Logf("warn", "canceled")
	case parent.Err() != nil:
		// Shutting down: leave it queued so the next start resumes it.
		e.db.Model(&j).Updates(map[string]any{"status": models.JobQueued})
		return
	default:
		j.Status = models.JobFailed
		j.Error = err.Error()
		r.Logf("error", "%v", err)
	}
	e.db.Model(&j).Updates(map[string]any{"status": j.Status, "error": j.Error, "finished_at": j.FinishedAt})
	e.Broker.Publish(Event{Type: "job", JobID: j.ID, ServerID: j.ServerID, Data: j})
	for _, f := range e.onFinish {
		go f(&j)
	}
}

// Run is a job in progress, handed to its handler.
type Run struct {
	Job   *models.Job
	e     *Engine
	mu    sync.Mutex
	state map[string]any
}

func (r *Run) Params(v any) error { return json.Unmarshal([]byte(r.Job.Params), v) }

func (r *Run) Logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l := models.JobLog{JobID: r.Job.ID, Level: level, Message: msg, CreatedAt: time.Now()}
	r.e.db.Create(&l)
	r.e.Broker.Publish(Event{Type: "log", JobID: r.Job.ID, ServerID: r.Job.ServerID, Data: l})
}

func (r *Run) Info(format string, args ...any) { r.Logf("info", format, args...) }

// Step records the current step name for the UI.
func (r *Run) Step(name string) {
	r.Job.Step = name
	r.e.db.Model(r.Job).Update("step", name)
	r.e.Broker.Publish(Event{Type: "job", JobID: r.Job.ID, ServerID: r.Job.ServerID, Data: r.Job})
	r.Info("▶ %s", name)
}

// Done reports whether a resumable step already completed in an earlier run.
func (r *Run) Done(step string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, _ := r.state["done:"+step].(bool)
	return v
}

// Mark persists that step completed.
func (r *Run) Mark(step string) { r.Set("done:"+step, true) }

func (r *Run) Set(key string, v any) {
	r.mu.Lock()
	r.state[key] = v
	b, _ := json.Marshal(r.state)
	r.mu.Unlock()
	r.Job.State = string(b)
	r.e.db.Model(r.Job).Update("state", r.Job.State)
}

func (r *Run) GetString(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, _ := r.state[key].(string)
	return s
}

// SetServer attaches the job to a server created during the job.
func (r *Run) SetServer(id uint) {
	r.Job.ServerID = &id
	r.e.db.Model(r.Job).Update("server_id", id)
}

// PublishServer tells live clients a server row changed.
func (r *Run) PublishServer(s *models.Server) {
	r.e.Broker.Publish(Event{Type: "server", ServerID: &s.ID, Data: s})
}
