package controllers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/settings"
)

func TestBalancerModeAPI(t *testing.T) {
	a, _ := smartApp(t)
	app := fiber.New()
	app.Get("/balancer-modes", a.ListModes)
	app.Post("/balancer-modes", a.CreateMode)
	app.Post("/balancer-modes/reorder", a.ReorderModes)
	app.Put("/balancer-modes/only", a.SetNodeOnly)
	app.Put("/balancer-modes/:id", a.UpdateMode)
	app.Delete("/balancer-modes/:id", a.DeleteMode)

	// Node-only without a mode would leave vpn-pannel empty.
	if code, body := call(t, app, "PUT", "/balancer-modes/only", `{"enabled":true}`); code != 400 {
		t.Fatalf("node-only without a mode: %d %s", code, body)
	}

	for _, bad := range []string{
		`{"name":"has space"}`, `{"name":"x","strategy":"fastest"}`, `{"name":"x","probe_interval":"2s"}`,
		`{"name":"x","ads_mode":"maybe"}`, `{"name":"x","location_ids":"999"}`,
	} {
		if code, body := call(t, app, "POST", "/balancer-modes", bad); code != 400 {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	mk := func(body string) models.BalancerMode {
		t.Helper()
		code, res := call(t, app, "POST", "/balancer-modes", body)
		var m models.BalancerMode
		if err := json.Unmarshal([]byte(res), &m); code != 200 || err != nil || m.ID == 0 {
			t.Fatalf("create %s: %d %s", body, code, res)
		}
		return m
	}
	first := mk(`{"name":"all-auto","probe_interval":"1m","ads_mode":"both"}`)
	second := mk(`{"name":"fast","strategy":"leastLoad"}`)
	if !first.Enabled || first.Priority >= second.Priority {
		t.Fatalf("new modes: %+v, %+v", first, second)
	}
	// A name another mode has, or its ads copy would get.
	for _, name := range []string{"ALL-AUTO", "all-auto-ads"} {
		if code, body := call(t, app, "POST", "/balancer-modes", `{"name":"`+name+`"}`); code != 409 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// The switch alone keeps the rest.
	if code, body := call(t, app, "PUT", "/balancer-modes/"+jsonNum(first.ID), `{"enabled":false}`); code != 200 ||
		!strings.Contains(body, `"probe_interval":"1m"`) || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("switch off: %d %s", code, body)
	}
	code, body := call(t, app, "POST", "/balancer-modes/reorder", `{"ids":[`+jsonNum(second.ID)+`,`+jsonNum(first.ID)+`]}`)
	var list struct {
		Only  bool                  `json:"only"`
		Modes []models.BalancerMode `json:"modes"`
	}
	if err := json.Unmarshal([]byte(body), &list); code != 200 || err != nil || len(list.Modes) != 2 || list.Modes[0].Name != "fast" {
		t.Fatalf("reorder: %d %s", code, body)
	}

	// Node-only: stored, and its job queued.
	if code, body := call(t, app, "PUT", "/balancer-modes/only", `{"enabled":true}`); code != 202 || !strings.Contains(body, orchestrator.KindNodeOnly) {
		t.Fatalf("node-only on: %d %s", code, body)
	}
	if !a.Settings.Bool(settings.NodeOnly) {
		t.Fatal("node-only was not stored")
	}
	if code, _ := call(t, app, "PUT", "/balancer-modes/only", `{"enabled":true}`); code != 409 {
		t.Fatalf("node-only on twice: %d", code)
	}
	if code, _ := call(t, app, "DELETE", "/balancer-modes/"+jsonNum(second.ID), ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	_, body = call(t, app, "GET", "/balancer-modes", "")
	if strings.Contains(body, `"fast"`) || !strings.Contains(body, `"only":true`) {
		t.Fatalf("list after delete: %s", body)
	}
}
