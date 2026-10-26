package bot

import (
	"bytes"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0:                        "0 B",
		1023:                     "1023 B",
		1536:                     "1.5 KB",
		390 * 1024 * 1024 * 1024: "390.0 GB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMainMenuButtonsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range mainMenu().Keyboard {
		for _, b := range row {
			if b.Text == "" || seen[b.Text] {
				t.Fatalf("empty or duplicate button %q", b.Text)
			}
			seen[b.Text] = true
		}
	}
	if len(seen) != 6 {
		t.Fatalf("got %d buttons, want 6", len(seen))
	}
}

func TestQRIsPNG(t *testing.T) {
	png, err := qrcode.Encode("vless://00000000-0000-0000-0000-000000000000@1.2.3.4:443?type=tcp&security=reality#x", qrcode.Medium, 512)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("qr: %v", err)
	}
}
