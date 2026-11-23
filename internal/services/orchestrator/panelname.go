package orchestrator

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"microserver/internal/models"
)

var namePrefixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidNamePrefix checks a location's vpn-pannel name prefix; empty is fine.
func ValidNamePrefix(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > 30 || !namePrefixRe.MatchString(p) || strings.HasSuffix(p, "-") {
		return errors.New("پیشوند فقط حروف انگلیسی، عدد، - و _ می‌پذیرد (حداکثر ۳۰ حرف، بدون - در انتها)")
	}
	return nil
}

// panelBaseName is the name a server's configs get in vpn-pannel when its
// location has a name prefix: prefix-N, N being the lowest number no live
// server of that prefix holds. It returns "" when the server has no prefixed
// location, and keeps the name once given so retries and IP changes reuse it.
func (o *Orchestrator) panelBaseName(s *models.Server) string {
	if s.LocationID == 0 {
		return ""
	}
	var loc models.Location
	if err := o.DB.First(&loc, s.LocationID).Error; err != nil || loc.NamePrefix == "" {
		return ""
	}
	prefix := loc.NamePrefix + "-"
	if strings.HasPrefix(s.PanelName, prefix) {
		return s.PanelName
	}
	o.nameMu.Lock()
	defer o.nameMu.Unlock()
	var taken []string
	o.DB.Model(&models.Server{}).
		Where("panel_name LIKE ? AND status <> ? AND id <> ?", prefix+"%", models.ServerDeleted, s.ID).
		Pluck("panel_name", &taken)
	used := map[string]bool{}
	for _, t := range taken {
		used[t] = true
	}
	n := 1
	for used[fmt.Sprintf("%s%d", prefix, n)] {
		n++
	}
	s.PanelName = fmt.Sprintf("%s%d", prefix, n)
	o.DB.Model(s).Update("panel_name", s.PanelName)
	return s.PanelName
}
