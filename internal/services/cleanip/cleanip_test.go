package cleanip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"microserver/internal/models"
)

func TestParse(t *testing.T) {
	js := `{"last_update":"x","ipv4":[{"ip":"172.66.213.38","operator":"MCI","provider":"ircf.space"},
		{"ip":"203.32.121.53","operator":"MTN"},{"ip":"bad ip","operator":"MTN"}],"ipv6":[]}`
	got, err := Parse([]byte(js))
	if err != nil || len(got) != 2 || got[0] != (Entry{"172.66.213.38", "MCI"}) || got[1].Operator != "MTN" {
		t.Fatalf("json: %+v %v", got, err)
	}
	txt := "# clean\n104.16.1.1\n104.16.1.2, mtn\r\nspeed.Example.com\tMCI # domain\n104.16.1.1\nnot_a_host\n"
	got, err = Parse([]byte(txt))
	if err != nil || len(got) != 3 || got[1] != (Entry{"104.16.1.2", "MTN"}) || got[2] != (Entry{"speed.example.com", "MCI"}) {
		t.Fatalf("text: %+v %v", got, err)
	}
}

func testDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.CleanIP{}, &models.CleanIPSource{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSyncKeepsManualAndDropsGone(t *testing.T) {
	body := `{"ipv4":[{"ip":"1.1.1.1","operator":"MCI"},{"ip":"2.2.2.2","operator":"MTN"},{"ip":"3.3.3.3","operator":"RTL"},{"ip":"9.9.9.9","operator":"MCI"}]}`
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	db := testDB(t)
	db.Create(&models.CleanIPSource{URL: srv.URL, Builtin: true, Enabled: true})
	db.Create(&models.CleanIP{Address: "9.9.9.9", Operator: "X", Source: Manual, Enabled: true})
	s := &Syncer{DB: db, HTTP: srv.Client(), Operators: func() []string { return []string{"mci", "MTN"} }}

	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []models.CleanIP
	db.Order("address").Find(&rows)
	if len(rows) != 3 || rows[0].Address != "1.1.1.1" || rows[1].Address != "2.2.2.2" || rows[2].Source != Manual || rows[2].Operator != "X" {
		t.Fatalf("after first sync: %+v", rows)
	}
	db.Model(&models.CleanIP{}).Where("address = ?", "1.1.1.1").Update("enabled", false)

	body = `{"ipv4":[{"ip":"1.1.1.1","operator":"MCI"}]}`
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows = nil
	db.Order("address").Find(&rows)
	if len(rows) != 2 || rows[0].Address != "1.1.1.1" || rows[0].Enabled || rows[1].Address != "9.9.9.9" {
		t.Fatalf("after second sync: %+v", rows)
	}

	fail = true
	if err := s.Sync(context.Background()); err == nil {
		t.Fatal("want error")
	}
	var n int64
	db.Model(&models.CleanIP{}).Count(&n)
	var src models.CleanIPSource
	db.First(&src)
	if n != 2 || src.LastError == "" {
		t.Fatalf("failed fetch: %d rows, error %q", n, src.LastError)
	}
}

func TestPick(t *testing.T) {
	db := testDB(t)
	yes, no := true, false
	db.Create(&[]models.CleanIP{
		{Address: "1.0.0.1", Enabled: true},
		{Address: "1.0.0.2", Enabled: true, CheckOK: &no},
		{Address: "1.0.0.3", Enabled: true, CheckOK: &yes},
		{Address: "1.0.0.4", Enabled: false, CheckOK: &yes},
	})
	got, err := Pick(db, 5)
	if err != nil || len(got) != 2 || got[0].Address != "1.0.0.3" || got[1].Address != "1.0.0.1" {
		t.Fatalf("%+v %v", got, err)
	}
}
