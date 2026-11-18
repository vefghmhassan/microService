package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	"microserver/internal/database"
	"microserver/internal/models"
	"microserver/internal/services/secret"
)

func initKey(t *testing.T, b byte) {
	t.Helper()
	if err := secret.Init(bytes.Repeat([]byte{b}, 32)); err != nil {
		t.Fatal(err)
	}
}

func openDB(t *testing.T, dir string) *gorm.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
	})
	return db
}

func TestModelsCovered(t *testing.T) {
	typ := func(list []any) map[reflect.Type]bool {
		out := map[reflect.Type]bool{}
		for _, m := range list {
			out[reflect.TypeOf(m)] = true
		}
		return out
	}
	have := typ(append(ConfigModels(), HistoryModels()...))
	want := typ(models.All())
	for k := range want {
		if !have[k] {
			t.Errorf("%v is in models.All() but not in the backup lists", k)
		}
	}
	for k := range have {
		if !want[k] {
			t.Errorf("%v is in the backup lists but not in models.All()", k)
		}
	}
}

func seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	u := models.AdminUser{Username: "boss"}
	if err := u.SetPassword("pw-123456"); err != nil {
		t.Fatal(err)
	}
	ok := true
	for _, v := range []any{
		&u,
		&models.ProviderAccount{Name: "hz", Type: "hetzner", Credentials: "api-key-xyz", Enabled: true},
		&models.CloudflareAccount{Name: "cf", Token: "cf-token", AcmeEmail: "a@b.c", Enabled: true},
		&models.Server{Name: "s1", ProviderAccountID: 1, IPv4: "1.2.3.4", Status: models.ServerActive,
			XUIPassword: "xui-pass", XUIToken: "xui-token", LastCheckAt: &now, LastCheckOK: &ok},
		&models.Destination{Name: "d", URL: "https://x", Password: "dest-pass", Enabled: true},
		&models.Setting{Key: "telegram.token", Value: mustEnc(t, "tg-token")},
		&models.Setting{Key: "check.min_ok", Value: "3"},
		&models.Job{Kind: "check", Status: "done"},
	} {
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("%T: %v", v, err)
		}
	}
}

func mustEnc(t *testing.T, s string) string {
	v, err := secret.Encrypt(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRoundTripAcrossMasterKeys(t *testing.T) {
	for _, pw := range []string{"", "s3cret"} {
		t.Run("password="+pw, func(t *testing.T) {
			initKey(t, 1)
			srcDir := t.TempDir()
			src := openDB(t, srcDir)
			seed(t, src)
			if err := os.WriteFile(filepath.Join(srcDir, sshKeyFile), []byte("PRIVATE KEY"), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := Export(src, srcDir, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := f.Tables["jobs"]; ok {
				t.Fatal("history exported without asking")
			}
			raw, err := Encode(f, pw)
			if err != nil {
				t.Fatal(err)
			}
			if IsEncrypted(raw) != (pw != "") {
				t.Fatalf("encrypted=%v with password %q", IsEncrypted(raw), pw)
			}
			if pw != "" && bytes.Contains(raw, []byte("boss")) {
				t.Fatal("plain text in an encrypted backup")
			}

			// Another install: another master key, data of its own.
			initKey(t, 2)
			dstDir := t.TempDir()
			dst := openDB(t, dstDir)
			if err := dst.Create(&models.ProviderAccount{Name: "other", Type: "vultr"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := dst.Create(&models.Job{Kind: "old", Status: "done"}).Error; err != nil {
				t.Fatal(err)
			}
			g, err := Decode(raw, pw)
			if err != nil {
				t.Fatal(err)
			}
			if err := Stage(dstDir, g); err != nil {
				t.Fatal(err)
			}
			applied, err := ApplyPending(dst, dstDir)
			if err != nil || !applied {
				t.Fatalf("applied=%v err=%v", applied, err)
			}
			if _, err := os.Stat(filepath.Join(dstDir, PendingFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pending file left behind")
			}

			var provs []models.ProviderAccount
			dst.Find(&provs)
			if len(provs) != 1 || provs[0].Name != "hz" || provs[0].Credentials != "api-key-xyz" {
				t.Fatalf("providers: %+v", provs)
			}
			var cf models.CloudflareAccount
			if err := dst.First(&cf).Error; err != nil || cf.Token != "cf-token" {
				t.Fatalf("cloudflare: %+v %v", cf, err)
			}
			var s models.Server
			if err := dst.First(&s).Error; err != nil {
				t.Fatal(err)
			}
			if s.XUIPassword != "xui-pass" || s.XUIToken != "xui-token" || s.LastCheckAt == nil || s.LastCheckOK == nil || !*s.LastCheckOK {
				t.Fatalf("server: %+v", s)
			}
			var d models.Destination
			if dst.First(&d); d.Password != "dest-pass" {
				t.Fatalf("destination password %q", d.Password)
			}
			var u models.AdminUser
			if dst.First(&u); u.Username != "boss" || !u.CheckPassword("pw-123456") {
				t.Fatalf("admin: %+v", u)
			}
			var tg models.Setting
			dst.First(&tg, "key = ?", "telegram.token")
			if plain, err := secret.Decrypt(tg.Value); err != nil || plain != "tg-token" {
				t.Fatalf("setting: %q %v", plain, err)
			}
			var jobs int64
			dst.Model(&models.Job{}).Count(&jobs)
			if jobs != 0 {
				t.Fatalf("history not cleared: %d jobs", jobs)
			}
			key, _ := os.ReadFile(filepath.Join(dstDir, sshKeyFile))
			if string(key) != "PRIVATE KEY" {
				t.Fatalf("ssh key %q", key)
			}
			// New rows get IDs after the imported ones.
			p := models.ProviderAccount{Name: "new", Type: "vultr"}
			if err := dst.Create(&p).Error; err != nil || p.ID <= provs[0].ID {
				t.Fatalf("new id %d: %v", p.ID, err)
			}
		})
	}
}

func TestHistoryIncluded(t *testing.T) {
	initKey(t, 1)
	dir := t.TempDir()
	db := openDB(t, dir)
	seed(t, db)
	f, err := Export(db, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tables["jobs"].Rows) != 1 {
		t.Fatalf("jobs: %+v", f.Tables["jobs"])
	}
}

func TestWrongPassword(t *testing.T) {
	initKey(t, 1)
	dir := t.TempDir()
	db := openDB(t, dir)
	seed(t, db)
	f, err := Export(db, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(f, "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw, "wrong"); !errors.Is(err, ErrPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := Decode(raw, ""); !errors.Is(err, ErrPassword) {
		t.Fatalf("no password: %v", err)
	}
	if _, err := Decode([]byte("garbage"), ""); err == nil {
		t.Fatal("garbage accepted")
	}
}
