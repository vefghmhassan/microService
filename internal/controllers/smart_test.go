package controllers

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

func smartApp(t *testing.T) (*App, *fiber.App) {
	t.Helper()
	a, _ := subApp(t)
	orchestrator.SmartDelay = time.Hour
	a.Engine = jobs.NewEngine(a.DB, 1)
	a.Orch = orchestrator.New(a.DB, a.Settings, nil, a.Engine)
	a.Orch.Register()
	if err := orchestrator.SeedSmartRules(a.DB); err != nil {
		t.Fatal(err)
	}
	var in models.InboundTemplate
	a.DB.Where("remark = ?", "DE").First(&in)
	now := time.Now()
	a.DB.Model(&models.Server{}).Where("name = ?", "a").Updates(map[string]any{"uploaded_at": &now, "uploaded_inbounds": jsonNum(in.ID), "uploaded_ip": "10.0.0.1"})

	app := fiber.New()
	app.Get("/smart-profiles", a.ListSmartProfiles)
	app.Post("/smart-profiles", a.CreateSmartProfile)
	app.Post("/smart-profiles/preview", a.PreviewSmartProfile)
	app.Put("/smart-profiles/:id", a.UpdateSmartProfile)
	app.Get("/smart-profiles/:id/preview", a.PreviewSmartProfile)
	app.Get("/smart-rules", a.ListSmartRules)
	app.Post("/smart-rules", a.CreateSmartRule)
	app.Delete("/smart-rules/:id", a.DeleteSmartRule)
	return a, app
}

func jsonNum(id uint) string { b, _ := json.Marshal(id); return string(b) }

func call(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSmartProfileAPI(t *testing.T) {
	_, app := smartApp(t)

	if code, body := call(t, app, "POST", "/smart-profiles", `{"name":"x","probe_interval":"10"}`); code != 400 || !strings.Contains(body, "مدت زمان") {
		t.Fatalf("bad duration accepted: %d %s", code, body)
	}
	code, body := call(t, app, "POST", "/smart-profiles", `{"name":"per","strategy":"leastLoad","obs_kind":"burst"}`)
	if code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	var p models.SmartProfile
	json.Unmarshal([]byte(body), &p)
	if p.ID == 0 || !p.Enabled || p.Scope != models.SmartScopeServer || p.NameTemplate != "{base}-smart" || p.ProbeURL == "" {
		t.Fatalf("defaults: %+v", p)
	}

	// The switch alone keeps everything else.
	code, body = call(t, app, "PUT", "/smart-profiles/"+jsonNum(p.ID), `{"enabled":false}`)
	var q models.SmartProfile
	json.Unmarshal([]byte(body), &q)
	if code != 200 || q.Enabled || q.Name != "per" || q.Strategy != "leastLoad" || q.ObsKind != "burst" {
		t.Fatalf("toggle: %d %+v", code, q)
	}

	code, body = call(t, app, "GET", "/smart-profiles/"+jsonNum(p.ID)+"/preview", "")
	if code != 200 {
		t.Fatalf("preview: %d %s", code, body)
	}
	var pv []orchestrator.SmartPreview
	json.Unmarshal([]byte(body), &pv)
	// a is active and uploaded, b is filtered, c is deleted.
	if len(pv) != 2 {
		t.Fatalf("preview targets: %+v", pv)
	}
	if pv[0].Name != "de-a-smart" || pv[0].Outbounds != 1 || !strings.Contains(pv[0].JSON, `"burstObservatory"`) {
		t.Fatalf("preview a: %+v", pv[0])
	}
	if pv[1].Off == "" || pv[1].JSON != "" {
		t.Fatalf("filtered server built: %+v", pv[1])
	}

	code, body = call(t, app, "POST", "/smart-profiles/preview", `{"name":"all","scope":"all","family":"v6"}`)
	json.Unmarshal([]byte(body), &pv)
	if code != 200 || len(pv) != 1 || pv[0].Off == "" {
		t.Fatalf("v6 preview: %d %s", code, body)
	}
}

func TestSmartRuleAPI(t *testing.T) {
	a, app := smartApp(t)
	var builtin models.SmartRule
	a.DB.Where("builtin = ?", true).First(&builtin)
	if code, _ := call(t, app, "DELETE", "/smart-rules/"+jsonNum(builtin.ID), ""); code != 409 {
		t.Fatalf("built-in rule deleted: %d", code)
	}
	if code, _ := call(t, app, "POST", "/smart-rules", `{"name":"empty"}`); code != 400 {
		t.Fatalf("empty rule accepted: %d", code)
	}
	code, body := call(t, app, "POST", "/smart-rules", `{"name":"chatgpt","domains":"domain:openai.com\ndomain:chatgpt.com"}`)
	if code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	var r models.SmartRule
	json.Unmarshal([]byte(body), &r)
	if r.Builtin || !r.Enabled || r.Order <= builtin.Order {
		t.Fatalf("rule %+v", r)
	}
	if code, _ := call(t, app, "DELETE", "/smart-rules/"+jsonNum(r.ID), ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
}
