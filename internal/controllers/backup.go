package controllers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/services/backup"
	"microserver/internal/services/settings"
)

// maxBackupUpload caps an uploaded backup file.
const maxBackupUpload = 60 << 20

// ExportBackup returns a full backup as a file download.
func (a *App) ExportBackup(c *fiber.Ctx) error {
	var req struct {
		Password string `json:"password"`
		History  bool   `json:"history"`
	}
	if err := c.BodyParser(&req); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	raw, err := a.buildBackup(req.History, req.Password)
	if err != nil {
		return errJSON(c, fiber.StatusInternalServerError, err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/octet-stream")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, backup.FileName()))
	return c.Send(raw)
}

func (a *App) buildBackup(history bool, password string) ([]byte, error) {
	f, err := backup.Export(a.DB, a.Cfg.DataDir, history)
	if err != nil {
		return nil, err
	}
	return backup.Encode(f, password)
}

// readBackupUpload decodes the multipart "file" with the "password" field.
func readBackupUpload(c *fiber.Ctx) (*backup.File, bool, error) {
	fh, err := c.FormFile("file")
	if err != nil {
		return nil, false, errors.New("فایل بکاپ انتخاب نشده")
	}
	if fh.Size > maxBackupUpload {
		return nil, false, errors.New("فایل بکاپ بیش از حد بزرگ است")
	}
	fd, err := fh.Open()
	if err != nil {
		return nil, false, err
	}
	defer fd.Close()
	raw, err := io.ReadAll(io.LimitReader(fd, maxBackupUpload+1))
	if err != nil {
		return nil, false, err
	}
	f, err := backup.Decode(raw, c.FormValue("password"))
	return f, backup.IsEncrypted(raw), err
}

// InspectBackup decodes an uploaded backup and describes it, changing nothing.
func (a *App) InspectBackup(c *fiber.Ctx) error {
	f, enc, err := readBackupUpload(c)
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(f.Summary(enc))
}

// ImportBackup stages an uploaded backup and restarts the panel, which applies
// it on boot. The current state is saved to backups-panel first.
func (a *App) ImportBackup(c *fiber.Ctx) error {
	if a.Restart == nil {
		return errJSON(c, fiber.StatusNotImplemented, "restart is not available")
	}
	f, enc, err := readBackupUpload(c)
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	safety, err := backup.SaveLocal(a.DB, a.Cfg.DataDir, "pre-import")
	if err != nil {
		return errJSON(c, fiber.StatusInternalServerError, "بکاپ از وضعیت فعلی ناموفق بود: "+err.Error())
	}
	if err := backup.Stage(a.Cfg.DataDir, f); err != nil {
		return errJSON(c, fiber.StatusInternalServerError, err.Error())
	}
	log.Printf("backup: import staged by %s (safety copy %s); restarting", source(c), safety)
	go func() {
		// Let the response reach the browser first.
		time.Sleep(time.Second)
		a.Restart()
	}()
	return c.JSON(fiber.Map{"ok": true, "restarting": true, "safety_backup": safety, "summary": f.Summary(enc)})
}

// SendBackupTelegram sends a backup to the bot's chats now, with the
// scheduled-backup settings.
func (a *App) SendBackupTelegram(c *fiber.Ctx) error {
	if a.SendBackup == nil {
		return errJSON(c, fiber.StatusNotImplemented, "telegram is not available")
	}
	if err := a.SendBackup(); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "at": a.Settings.Get(settings.BackupLastAt)})
}
