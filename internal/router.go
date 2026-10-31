package internal

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"

	"microserver/internal/controllers"
	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

// Register mounts the JSON API under /api/v1 and the dashboard (the built SPA)
// everywhere else.
func Register(app *fiber.App, a *controllers.App, web fs.FS) {
	api := app.Group("/api/v1")
	api.Post("/auth/login", a.Login)
	api.Post("/auth/logout", a.Logout)
	api.Get("/healthz", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) })

	exists := func(id uint) bool {
		var n int64
		a.DB.Model(&models.AdminUser{}).Where("id = ?", id).Count(&n)
		return n > 0
	}
	p := api.Group("", a.Auth.Required(exists))

	p.Get("/auth/me", a.Me)
	p.Post("/auth/password", a.ChangePassword)
	p.Get("/meta", a.Meta)
	p.Get("/meta/xui-releases", a.XUIReleases)
	p.Get("/dashboard", a.Dashboard)
	p.Get("/events", a.Events)

	p.Get("/providers/types", a.ProviderTypes)
	p.Get("/providers", a.ListProviders)
	p.Post("/providers", a.CreateProvider)
	p.Put("/providers/:id", a.UpdateProvider)
	p.Delete("/providers/:id", a.DeleteProvider)
	p.Post("/providers/:id/test", a.TestProvider)
	p.Get("/providers/:id/regions", a.ProviderRegions)
	p.Get("/providers/:id/plans", a.ProviderPlans)

	p.Get("/servers", a.ListServers)
	p.Post("/servers", a.CreateServer)
	p.Get("/servers/:id", a.GetServer)
	p.Put("/servers/:id", a.UpdateServer)
	p.Delete("/servers/:id", a.ServerAction(orchestrator.KindDelete))
	p.Post("/servers/:id/rotate", a.ServerAction(orchestrator.KindRotate))
	p.Post("/servers/:id/check", a.ServerAction(orchestrator.KindCheck))
	p.Post("/servers/:id/install", a.ServerAction(orchestrator.KindInstall))
	p.Post("/servers/:id/sync", a.ServerAction(orchestrator.KindSync))
	p.Post("/servers/:id/upload", a.ServerAction(orchestrator.KindUpload))
	p.Post("/servers/:id/xray-version", a.ServerAction(orchestrator.KindXrayVersion))
	p.Post("/servers/:id/panel-version", a.ServerAction(orchestrator.KindPanelVersion))
	p.Get("/servers/:id/links", a.ServerLinks)
	p.Get("/servers/:id/checks", a.ServerChecks)
	p.Get("/servers/:id/live", a.ServerLive)
	p.Get("/servers/:id/panel-access", a.ServerPanelAccess)
	p.Get("/servers/:id/inbounds", a.ServerInbounds)
	p.Post("/servers/:id/stats", a.RefreshStats)
	p.Post("/servers/:id/replace", a.ReplaceServer)
	p.Post("/servers/:id/panel-access", a.ServerAction(orchestrator.KindPanelAccess))
	p.Post("/servers/:id/cert", a.ServerAction(orchestrator.KindCertPush))
	p.Post("/servers/:id/dns", a.ServerAction(orchestrator.KindDNSSync))

	p.Get("/locations", a.ListLocations)
	p.Post("/locations", a.CreateLocation)
	p.Post("/locations/reorder", a.ReorderLocations)
	p.Put("/locations/:id", a.UpdateLocation)
	p.Delete("/locations/:id", a.DeleteLocation)

	p.Get("/cloudflare", a.ListCloudflare)
	p.Post("/cloudflare", a.CreateCloudflare)
	p.Get("/cloudflare/certificates", a.ListCertificates)
	p.Put("/cloudflare/:id", a.UpdateCloudflare)
	p.Delete("/cloudflare/:id", a.DeleteCloudflare)
	p.Post("/cloudflare/:id/test", a.TestCloudflare)
	p.Get("/cloudflare/:id/zones", a.CloudflareZones)
	p.Post("/cloudflare/:id/certificate", a.IssueCertificate)
	p.Post("/cloudflare/:id/cdn", a.SetupZoneCDN)
	p.Get("/clean-ips", a.ListCleanIPs)
	p.Post("/clean-ips", a.AddCleanIPs)
	p.Post("/clean-ips/sync", a.SyncCleanIPs)
	p.Post("/clean-ips/check", a.CheckCleanIPs)
	p.Post("/clean-ips/delete", a.DeleteCleanIPs)
	p.Post("/clean-ips/sources", a.AddCleanIPSource)
	p.Put("/clean-ips/sources/:id", a.UpdateCleanIPSource)
	p.Delete("/clean-ips/sources/:id", a.DeleteCleanIPSource)
	p.Put("/clean-ips/:id", a.UpdateCleanIP)
	p.Delete("/clean-ips/:id", a.DeleteCleanIP)

	p.Get("/ips", a.ListIPs)
	p.Delete("/ips/:id", a.ReleaseIP)

	p.Get("/checks", a.ListChecks)
	p.Post("/checks", a.RunCheck)
	p.Post("/checks/all", a.EnqueueGlobal(orchestrator.KindCheckAll))

	p.Get("/template", a.GetTemplate)
	p.Put("/template/xray", a.PutXrayTemplate)
	p.Post("/template/inbounds", a.CreateInbound)
	p.Put("/template/inbounds/:id", a.UpdateInbound)
	p.Delete("/template/inbounds/:id", a.DeleteInbound)
	p.Get("/template/inbounds/:id/links", a.InboundLinks)
	p.Get("/template/x25519", a.NewX25519)
	p.Get("/template/xray/base", a.XrayBase)
	p.Post("/template/outbounds/test", a.TestOutbounds)
	p.Post("/template/reality/scan", a.ScanReality)
	p.Post("/template/reality/scan-many", a.ScanRealityMany)
	p.Post("/template/sync", a.EnqueueGlobal(orchestrator.KindSyncAll))

	p.Get("/jobs", a.ListJobs)
	p.Get("/jobs/:id", a.GetJob)
	p.Post("/jobs/:id/cancel", a.CancelJob)

	p.Get("/settings", a.GetSettings)
	p.Put("/settings", a.PutSettings)
	p.Get("/destinations", a.ListDestinations)
	p.Post("/destinations", a.CreateDestination)
	p.Put("/destinations/:id", a.UpdateDestination)
	p.Delete("/destinations/:id", a.DeleteDestination)
	p.Post("/destinations/:id/test", a.TestDestination)
	p.Post("/settings/test/telegram", a.TestTelegramHandler)
	p.Post("/destination/upload", a.UploadConfigs)

	p.Get("/sub", a.GetSub)
	p.Post("/sub/token", a.RenewSubToken)
	p.Get("/geo/report", a.GeoReport)
	p.Post("/geo/update", a.UpdateGeoIP)

	api.Use(func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
	})

	// The all-servers subscription, public but behind its secret token.
	app.Get("/sub/:token", a.Sub)

	// The SPA: real files are served as-is; every other path falls back to
	// index.html so client-side routes survive a reload.
	app.Use(filesystem.New(filesystem.Config{
		Root:         http.FS(web),
		Index:        "index.html",
		NotFoundFile: "index.html",
		MaxAge:       0,
		Next: func(c *fiber.Ctx) bool {
			return strings.HasPrefix(c.Path(), "/api/")
		},
	}))
}
