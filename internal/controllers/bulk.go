package controllers

import (
	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

// errServerState is a server refusing an action in its current state.
type errServerState string

func (e errServerState) Error() string { return string(e) }

// jobActions are the server-menu actions that are one job of a kind.
var jobActions = map[string]string{
	"rotate":     orchestrator.KindRotate,
	"check":      orchestrator.KindCheck,
	"install":    orchestrator.KindInstall,
	"sync":       orchestrator.KindSync,
	"upload":     orchestrator.KindUpload,
	"unpublish":  orchestrator.KindUnpublish,
	"cert":       orchestrator.KindCertPush,
	"dns":        orchestrator.KindDNSSync,
	"ip-cert":    orchestrator.KindIPCert,
	"node-join":  orchestrator.KindNodeJoin,
	"node-leave": orchestrator.KindNodeLeave,
}

// serverOp runs one server-menu action on s. Actions with no job (cancel a
// scheduled delete) return a nil job.
func (a *App) serverOp(s *models.Server, action string, params map[string]any, src string) (*models.Job, error) {
	gone := s.Status == models.ServerDeleted || s.Status == models.ServerDeleting
	switch action {
	case "delete":
		if s.Status == models.ServerDeleted {
			return nil, errServerState("server is deleted")
		}
		return a.Orch.DeleteServer(s.ID, src)
	case "replace":
		if gone {
			return nil, errServerState("server is deleted")
		}
		j, err := a.Orch.QueueReplace(s, "manual (panel)", src)
		if err != nil {
			return nil, errServerState(err.Error())
		}
		return j, nil
	case "schedule-delete":
		if gone {
			return nil, errServerState("server is deleted")
		}
		if s.DeleteAt != nil {
			return nil, errServerState("a deletion is already scheduled")
		}
		return a.Orch.ScheduleDelete(s.ID, a.scheduledDeleteDelay(), src)
	case "cancel-delete":
		if s.DeleteAt == nil {
			return nil, errServerState("no deletion is scheduled")
		}
		return nil, a.Orch.CancelScheduledDelete(s.ID)
	}
	kind, ok := jobActions[action]
	if !ok {
		return nil, errServerState("unknown action " + action)
	}
	if s.Status == models.ServerDeleted {
		return nil, errServerState("server is deleted")
	}
	if params == nil {
		params = map[string]any{}
	}
	return a.Engine.Enqueue(kind, &s.ID, params, src)
}

type bulkResult struct {
	ID    uint        `json:"id"`
	Name  string      `json:"name"`
	Job   *models.Job `json:"job,omitempty"`
	Error string      `json:"error,omitempty"`
}

// BulkServerAction runs one server-menu action on many servers. A server
// that refuses it (wrong state) does not stop the others; each gets its own
// result.
func (a *App) BulkServerAction(c *fiber.Ctx) error {
	var in struct {
		IDs    []uint         `json:"ids"`
		Action string         `json:"action"`
		Body   map[string]any `json:"body"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if len(in.IDs) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "ids required")
	}
	if len(in.IDs) > 500 {
		return errJSON(c, fiber.StatusBadRequest, "at most 500 servers at once")
	}
	switch in.Action {
	case "delete", "replace", "schedule-delete", "cancel-delete":
	default:
		if _, ok := jobActions[in.Action]; !ok {
			return errJSON(c, fiber.StatusBadRequest, "unknown action")
		}
	}
	var servers []models.Server
	if err := a.DB.Where("id IN ?", in.IDs).Find(&servers).Error; err != nil {
		return err
	}
	byID := make(map[uint]*models.Server, len(servers))
	for i := range servers {
		byID[servers[i].ID] = &servers[i]
	}
	src := source(c)
	out := make([]bulkResult, 0, len(in.IDs))
	seen := map[uint]bool{}
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		s := byID[id]
		if s == nil {
			out = append(out, bulkResult{ID: id, Error: "server not found"})
			continue
		}
		// Each server gets its own copy: the job queue keeps the params.
		var params map[string]any
		if in.Body != nil {
			params = make(map[string]any, len(in.Body))
			for k, v := range in.Body {
				params[k] = v
			}
		}
		j, err := a.serverOp(s, in.Action, params, src)
		r := bulkResult{ID: id, Name: s.Name, Job: j}
		if err != nil {
			// Reported per server, not as a 500 that hides what already ran.
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return c.JSON(out)
}
