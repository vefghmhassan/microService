package controllers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

func TestInboundOnReservedPanelPortsIsRefused(t *testing.T) {
	a, app := subApp(t)
	app.Post("/template/inbounds", a.CreateInbound)
	post := func(port string) int {
		body := `{"remark":"x` + port + `","protocol":"vless","port":` + port + `,"settings":"{}","stream_settings":"{\"network\":\"tcp\",\"security\":\"none\"}","sniffing":"{}"}`
		req := httptest.NewRequest("POST", "/template/inbounds", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	for _, p := range []string{"8443", "2096"} {
		if code := post(p); code != 409 {
			t.Fatalf("port %s: %d", p, code)
		}
	}
	a.Settings.Set(map[string]string{settings.XUICFPanelPort: "2053"})
	if code := post("2053"); code != 409 {
		t.Fatalf("configured panel port accepted: %d", code)
	}
	if code := post("2443"); code != 200 {
		t.Fatalf("free port refused: %d", code)
	}
	var n int64
	a.DB.Model(&models.InboundTemplate{}).Where("port = 2443").Count(&n)
	if n != 1 {
		t.Fatal("not saved")
	}
}
