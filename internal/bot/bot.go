// Package bot is the Telegram front end: the same actions as the dashboard
// (list, check, rotate IP, sync, delete, create) plus notifications.
package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	tg "github.com/go-telegram/bot"
	tgm "github.com/go-telegram/bot/models"
	qrcode "github.com/skip2/go-qrcode"
	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/settings"
)

type Manager struct {
	DB       *gorm.DB
	Settings *settings.Store
	Engine   *jobs.Engine
	Orch     *orchestrator.Orchestrator

	mu     sync.Mutex
	b      *tg.Bot
	cancel context.CancelFunc
	chats  map[int64]bool
}

// Start (re)starts the bot from the current settings; with no token it stops.
func (m *Manager) Start(parent context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel, m.b = nil, nil
	}
	token := m.Settings.Get(settings.TelegramToken)
	m.chats = map[int64]bool{}
	for _, s := range m.Settings.List(settings.TelegramChatIDs) {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			m.chats[id] = true
		}
	}
	if token == "" {
		return nil
	}
	httpClient := &http.Client{Timeout: 70 * time.Second}
	if p := m.Settings.Get(settings.TelegramProxy); p != "" {
		pu, err := url.Parse(p)
		if err != nil {
			return fmt.Errorf("telegram proxy: %w", err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
	}
	b, err := tg.New(token,
		tg.WithHTTPClient(60*time.Second, httpClient),
		tg.WithMiddlewares(m.guard),
		tg.WithDefaultHandler(m.help),
		tg.WithErrorsHandler(func(err error) { log.Printf("telegram: %v", err) }),
	)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	// Each action answers both its /command and its main-menu keyboard button.
	for _, h := range []struct {
		cmd, btn string
		fn       tg.HandlerFunc
	}{
		{"/servers", btnServers, m.cmdServers},
		{"/status", btnStatus, m.cmdStatus},
		{"/new", btnNew, m.cmdNew},
		{"/checkall", btnCheckAll, m.cmdCheckAll},
		{"/sync", btnSync, m.cmdSyncAll},
		{"/help", btnHelp, m.help},
	} {
		b.RegisterHandler(tg.HandlerTypeMessageText, h.cmd, tg.MatchTypeCommand, h.fn)
		b.RegisterHandler(tg.HandlerTypeMessageText, h.btn, tg.MatchTypeExact, h.fn)
	}
	b.RegisterHandler(tg.HandlerTypeCallbackQueryData, "", tg.MatchTypePrefix, m.callback)

	ctx, cancel := context.WithCancel(parent)
	m.b, m.cancel = b, cancel
	go b.Start(ctx)
	log.Printf("telegram bot started (%d allowed chats)", len(m.chats))
	return nil
}

func chatOf(u *tgm.Update) int64 {
	switch {
	case u.Message != nil:
		return u.Message.Chat.ID
	case u.CallbackQuery != nil && u.CallbackQuery.Message.Message != nil:
		return u.CallbackQuery.Message.Message.Chat.ID
	}
	return 0
}

// guard drops updates from chats that are not allowed. An unknown chat is told
// its id once, so the admin can paste it into the settings.
func (m *Manager) guard(next tg.HandlerFunc) tg.HandlerFunc {
	return func(ctx context.Context, b *tg.Bot, u *tgm.Update) {
		id := chatOf(u)
		m.mu.Lock()
		ok := m.chats[id]
		m.mu.Unlock()
		if !ok {
			if u.Message != nil {
				_, _ = b.SendMessage(ctx, &tg.SendMessageParams{ChatID: id,
					Text: fmt.Sprintf("دسترسی ندارید. شناسه این چت: <code>%d</code>\nآن را در تنظیمات پنل اضافه کنید.", id), ParseMode: tgm.ParseModeHTML})
			}
			return
		}
		next(ctx, b, u)
	}
}

// Notify sends text to every allowed chat.
func (m *Manager) Notify(text string) {
	if !m.Settings.Bool(settings.TelegramNotify) {
		return
	}
	m.mu.Lock()
	b := m.b
	chats := make([]int64, 0, len(m.chats))
	for id := range m.chats {
		chats = append(chats, id)
	}
	m.mu.Unlock()
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range chats {
		if _, err := b.SendMessage(ctx, &tg.SendMessageParams{ChatID: id, Text: text}); err != nil {
			log.Printf("telegram notify %d: %v", id, err)
		}
	}
}

// NotifyServer sends text about one server to every allowed chat, with
// buttons to change its IP, replace it, delete it or open it. The buttons go
// through the same confirmations as the server menu.
func (m *Manager) NotifyServer(s *models.Server, text string) {
	if !m.Settings.Bool(settings.TelegramNotify) {
		return
	}
	m.mu.Lock()
	b := m.b
	chats := make([]int64, 0, len(m.chats))
	for id := range m.chats {
		chats = append(chats, id)
	}
	m.mu.Unlock()
	if b == nil {
		return
	}
	kb := &tgm.InlineKeyboardMarkup{InlineKeyboard: [][]tgm.InlineKeyboardButton{
		{{Text: "🔄 تعویض IP", CallbackData: fmt.Sprintf("rot:%d", s.ID)}, {Text: "♻️ جایگزینی سرور", CallbackData: fmt.Sprintf("rpl:%d", s.ID)}},
		{{Text: "🗑 حذف", CallbackData: fmt.Sprintf("del:%d", s.ID)}, {Text: "🖥 جزئیات", CallbackData: fmt.Sprintf("srv:%d", s.ID)}},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range chats {
		if _, err := b.SendMessage(ctx, &tg.SendMessageParams{ChatID: id, Text: text, ReplyMarkup: kb}); err != nil {
			log.Printf("telegram notify %d: %v", id, err)
		}
	}
}

// Test sends a message to every allowed chat and reports the first failure.
func (m *Manager) Test() error {
	m.mu.Lock()
	b := m.b
	chats := make([]int64, 0, len(m.chats))
	for id := range m.chats {
		chats = append(chats, id)
	}
	m.mu.Unlock()
	if b == nil {
		return errors.New("توکن ربات تنظیم نشده است")
	}
	if len(chats) == 0 {
		return errors.New("هیچ chat id مجازی تنظیم نشده؛ به ربات پیام دهید تا شناسه را ببینید")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, id := range chats {
		if _, err := b.SendMessage(ctx, &tg.SendMessageParams{ChatID: id, Text: "✅ اتصال ربات microServer برقرار است"}); err != nil {
			return fmt.Errorf("chat %d: %w", id, err)
		}
	}
	return nil
}

// OnJobFinish reports failures; successes are announced by the flows.
func (m *Manager) OnJobFinish(j *models.Job) {
	if j.Status != models.JobFailed {
		return
	}
	name := ""
	if j.ServerID != nil {
		var s models.Server
		if m.DB.Select("name").First(&s, *j.ServerID).Error == nil {
			name = " — " + s.Name
		}
	}
	m.Notify(fmt.Sprintf("❌ کار %s #%d%s ناموفق بود:\n%s", kindFa(j.Kind), j.ID, name, truncate(j.Error, 500)))
}

// Main-menu keyboard buttons; their text is what the bot receives on a tap.
const (
	btnServers  = "🖥 سرورها"
	btnStatus   = "📊 وضعیت و مصرف"
	btnNew      = "➕ سرور جدید"
	btnCheckAll = "📡 چک همه از ایران"
	btnSync     = "🔁 اعمال تمپلیت"
	btnHelp     = "❓ راهنما"
)

func mainMenu() *tgm.ReplyKeyboardMarkup {
	return &tgm.ReplyKeyboardMarkup{
		Keyboard: [][]tgm.KeyboardButton{
			{{Text: btnServers}, {Text: btnStatus}},
			{{Text: btnNew}, {Text: btnCheckAll}},
			{{Text: btnSync}, {Text: btnHelp}},
		},
		ResizeKeyboard: true,
		IsPersistent:   true,
	}
}

func (m *Manager) send(ctx context.Context, b *tg.Bot, chat int64, text string, kb [][]tgm.InlineKeyboardButton) {
	var rm tgm.ReplyMarkup
	if kb != nil {
		rm = &tgm.InlineKeyboardMarkup{InlineKeyboard: kb}
	}
	m.sendMarkup(ctx, b, chat, text, rm)
}

func (m *Manager) sendMarkup(ctx context.Context, b *tg.Bot, chat int64, text string, rm tgm.ReplyMarkup) {
	p := &tg.SendMessageParams{ChatID: chat, Text: text, ParseMode: tgm.ParseModeHTML, ReplyMarkup: rm}
	if _, err := b.SendMessage(ctx, p); err != nil {
		log.Printf("telegram send: %v", err)
	}
}

func (m *Manager) help(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	if u.Message == nil {
		return
	}
	m.sendMarkup(ctx, b, u.Message.Chat.ID, strings.Join([]string{
		"<b>microServer</b> — از دکمه‌های پایین صفحه استفاده کنید:",
		btnServers + " — فهرست سرورها و عملیات هر سرور",
		btnStatus + " — خلاصه وضعیت، ترافیک و کاربران آنلاین",
		btnNew + " — ساخت سرور جدید (خودکار یا روی یک حساب)",
		btnCheckAll + " — چک همه سرورها از ایران",
		btnSync + " — اعمال تمپلیت روی همه پنل‌ها",
	}, "\n"), mainMenu())
}

func (m *Manager) cmdServers(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	var list []models.Server
	m.DB.Where("status <> ?", models.ServerDeleted).Order("id").Limit(60).Find(&list)
	if len(list) == 0 {
		m.send(ctx, b, u.Message.Chat.ID, "هیچ سروری وجود ندارد. /new", nil)
		return
	}
	var kb [][]tgm.InlineKeyboardButton
	for _, s := range list {
		kb = append(kb, []tgm.InlineKeyboardButton{{
			Text:         fmt.Sprintf("%s %s · %s", statusIcon(s.Status), s.Name, s.PublicIP()),
			CallbackData: fmt.Sprintf("srv:%d", s.ID),
		}})
	}
	m.send(ctx, b, u.Message.Chat.ID, "سرورها:", kb)
}

func (m *Manager) cmdStatus(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	type row struct {
		Status string
		N      int
	}
	var rows []row
	m.DB.Model(&models.Server{}).Select("status, count(*) as n").Where("status <> ?", models.ServerDeleted).Group("status").Scan(&rows)
	var lines []string
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%s %s: %d", statusIcon(r.Status), statusFa(r.Status), r.N))
	}
	var active int64
	m.DB.Model(&models.Job{}).Where("status IN ?", []string{models.JobQueued, models.JobRunning}).Count(&active)
	lines = append(lines, fmt.Sprintf("⚙️ کارهای در جریان: %d", active))
	var t struct{ Up, Down, Online int64 }
	m.DB.Model(&models.Server{}).Select("COALESCE(SUM(traffic_up),0) AS up, COALESCE(SUM(traffic_down),0) AS down, COALESCE(SUM(online_count),0) AS online").
		Where("status <> ?", models.ServerDeleted).Scan(&t)
	lines = append(lines, fmt.Sprintf("📈 ترافیک کل: %s · 👥 آنلاین: %d", humanBytes(t.Up+t.Down), t.Online))
	m.send(ctx, b, u.Message.Chat.ID, strings.Join(lines, "\n"), nil)
}

func (m *Manager) cmdNew(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	var accs []models.ProviderAccount
	m.DB.Where("enabled = ? AND default_region <> '' AND default_plan <> ''", true).Find(&accs)
	var locs int64
	m.DB.Model(&models.Location{}).Where("enabled = ?", true).Count(&locs)
	if len(accs) == 0 && locs == 0 {
		m.send(ctx, b, u.Message.Chat.ID, "نه لوکیشنی تعریف شده و نه حسابی با منطقه و پلن پیش‌فرض. از پنل تنظیم کنید.", nil)
		return
	}
	var kb [][]tgm.InlineKeyboardButton
	if locs > 0 {
		kb = append(kb, []tgm.InlineKeyboardButton{{
			Text: fmt.Sprintf("🤖 خودکار — طبق اولویت %d لوکیشن", locs), CallbackData: "newauto:1",
		}})
	}
	for _, a := range accs {
		kb = append(kb, []tgm.InlineKeyboardButton{{
			Text:         fmt.Sprintf("%s (%s · %s/%s)", a.Name, a.Type, a.DefaultRegion, a.DefaultPlan),
			CallbackData: fmt.Sprintf("new:%d", a.ID),
		}})
	}
	m.send(ctx, b, u.Message.Chat.ID, "سرور جدید روی کدام حساب ساخته شود؟", kb)
}

func (m *Manager) cmdCheckAll(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	if _, err := m.Engine.Enqueue(orchestrator.KindCheckAll, nil, struct{}{}, "telegram"); err != nil {
		m.send(ctx, b, u.Message.Chat.ID, "خطا: "+html.EscapeString(err.Error()), nil)
		return
	}
	m.send(ctx, b, u.Message.Chat.ID, "📡 چک همه سرورها در صف قرار گرفت", nil)
}

func (m *Manager) cmdSyncAll(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	if _, err := m.Engine.Enqueue(orchestrator.KindSyncAll, nil, struct{}{}, "telegram"); err != nil {
		m.send(ctx, b, u.Message.Chat.ID, "خطا: "+html.EscapeString(err.Error()), nil)
		return
	}
	m.send(ctx, b, u.Message.Chat.ID, "🔁 اعمال تمپلیت روی همه سرورها در صف قرار گرفت", nil)
}

func (m *Manager) callback(ctx context.Context, b *tg.Bot, u *tgm.Update) {
	q := u.CallbackQuery
	if q == nil {
		return
	}
	_, _ = b.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	chat := chatOf(u)
	action, arg, _ := strings.Cut(q.Data, ":")
	// A third part, where present, is the IP family ("new:3:v6").
	arg, fam, _ := strings.Cut(arg, ":")
	id64, _ := strconv.ParseUint(arg, 10, 64)
	id := uint(id64)

	enqueue := func(kind string, params any, done string) {
		sid := id
		if _, err := m.Engine.Enqueue(kind, &sid, params, "telegram"); err != nil {
			m.send(ctx, b, chat, "خطا: "+html.EscapeString(err.Error()), nil)
			return
		}
		m.send(ctx, b, chat, done, nil)
	}

	switch action {
	case "srv":
		m.showServer(ctx, b, chat, id)
	case "rot":
		m.send(ctx, b, chat, "IP این سرور عوض شود؟", [][]tgm.InlineKeyboardButton{{
			{Text: "✅ بله، تعویض کن", CallbackData: fmt.Sprintf("rotok:%d", id)},
			{Text: "انصراف", CallbackData: fmt.Sprintf("srv:%d", id)},
		}})
	case "rotok":
		enqueue(orchestrator.KindRotate, orchestrator.RotateParams{Reason: "telegram"}, "🔄 تعویض IP در صف قرار گرفت؛ نتیجه را خبر می‌دهم.")
	case "chk":
		enqueue(orchestrator.KindCheck, struct{}{}, "📡 چک از ایران در صف قرار گرفت.")
	case "syn":
		enqueue(orchestrator.KindSync, struct{}{}, "🔁 اعمال تمپلیت در صف قرار گرفت.")
	case "upl":
		enqueue(orchestrator.KindUpload, struct{}{}, "⬆️ بارگذاری روی vpn-pannel در صف قرار گرفت.")
	case "lnk":
		m.sendLinks(ctx, b, chat, id)
	case "qr":
		m.sendQRs(ctx, b, chat, id)
	case "rpl":
		m.send(ctx, b, chat, "یک سرور جدید ساخته شود و کانفیگ‌ها به آن منتقل شوند، بعد این سرور حذف شود؟", [][]tgm.InlineKeyboardButton{{
			{Text: "♻️ بله، جایگزین کن", CallbackData: fmt.Sprintf("rplok:%d", id)},
			{Text: "انصراف", CallbackData: fmt.Sprintf("srv:%d", id)},
		}})
	case "rplok":
		var s models.Server
		if m.DB.First(&s, id).Error != nil || m.Orch == nil {
			return
		}
		if _, err := m.Orch.QueueReplace(&s, "manual (telegram)", "telegram"); err != nil {
			m.send(ctx, b, chat, "خطا: "+html.EscapeString(err.Error()), nil)
			return
		}
		m.send(ctx, b, chat, "♻️ جایگزینی سرور در صف قرار گرفت؛ نتیجه را خبر می‌دهم.", nil)
	case "del":
		m.send(ctx, b, chat, "⚠️ سرور برای همیشه حذف شود؟", [][]tgm.InlineKeyboardButton{{
			{Text: "🗑 بله، حذف کن", CallbackData: fmt.Sprintf("delok:%d", id)},
			{Text: "انصراف", CallbackData: fmt.Sprintf("srv:%d", id)},
		}})
	case "delok":
		enqueue(orchestrator.KindDelete, struct{}{}, "🗑 حذف در صف قرار گرفت.")
	case "new":
		var a models.ProviderAccount
		if m.DB.First(&a, id).Error != nil {
			return
		}
		m.send(ctx, b, chat, fmt.Sprintf("ساخت سرور روی %s در %s با پلن %s؟\nکانفیگ‌ها روی IPv4 باشند یا IPv6؟", html.EscapeString(a.Name), a.DefaultRegion, a.DefaultPlan),
			[][]tgm.InlineKeyboardButton{{
				{Text: "✅ بساز · IPv4", CallbackData: fmt.Sprintf("newok:%d:v4", id)},
				{Text: "✅ بساز · IPv6", CallbackData: fmt.Sprintf("newok:%d:v6", id)},
			}})
	case "newauto":
		if fam == "" {
			m.send(ctx, b, chat, "ساخت خودکار: کانفیگ‌ها روی IPv4 باشند یا IPv6؟", [][]tgm.InlineKeyboardButton{
				{{Text: "IPv4", CallbackData: "newauto:1:v4"}, {Text: "IPv6", CallbackData: "newauto:1:v6"}},
				{{Text: "طبق تنظیم هر لوکیشن", CallbackData: "newauto:1:loc"}},
			})
			return
		}
		p := orchestrator.AutoParams{Count: 1}
		if fam == models.FamilyV4 || fam == models.FamilyV6 {
			p.IPFamily = fam
		}
		if _, err := m.Engine.Enqueue(orchestrator.KindProvisionAuto, nil, p, "telegram"); err != nil {
			m.send(ctx, b, chat, "خطا: "+html.EscapeString(err.Error()), nil)
			return
		}
		m.send(ctx, b, chat, "🤖 ساخت خودکار شروع شد: لوکیشن‌ها به ترتیب امتحان می‌شوند و نتیجه را خبر می‌دهم.", nil)
	case "newok":
		p := orchestrator.ProvisionParams{AccountID: id, IPFamily: models.NormFamily(fam)}
		if _, err := m.Engine.Enqueue(orchestrator.KindProvision, nil, p, "telegram"); err != nil {
			m.send(ctx, b, chat, "خطا: "+html.EscapeString(err.Error()), nil)
			return
		}
		m.send(ctx, b, chat, fmt.Sprintf("🚀 ساخت سرور IP%s شروع شد؛ وقتی آماده شد خبر می‌دهم.", p.IPFamily), nil)
	}
}

func (m *Manager) showServer(ctx context.Context, b *tg.Bot, chat int64, id uint) {
	var s models.Server
	if m.DB.Preload("ProviderAccount").First(&s, id).Error != nil {
		m.send(ctx, b, chat, "سرور پیدا نشد", nil)
		return
	}
	check := "—"
	if s.LastCheckAt != nil {
		check = fmt.Sprintf("%s (%s)", map[bool]string{true: "✅ باز", false: "⛔️ بسته"}[s.LastCheckOK != nil && *s.LastCheckOK], s.LastCheckAt.Format("01-02 15:04"))
	}
	prov := ""
	if s.ProviderAccount != nil {
		prov = s.ProviderAccount.Name
	}
	traffic := humanBytes(s.TrafficUp + s.TrafficDown)
	if m.Orch != nil {
		if lim := m.Orch.TrafficLimit(&s); lim > 0 {
			traffic += " از " + humanBytes(lim)
		}
	}
	text := fmt.Sprintf("<b>%s</b>\nوضعیت: %s %s\nIP: <code>%s</code>%s\nارائه‌دهنده: %s · %s\n3x-ui: %s · xray: %s\nهمگام‌سازی: %s\nآخرین چک: %s\nتعداد تعویض IP: %d\n📈 ترافیک: %s · 👥 آنلاین: %d",
		html.EscapeString(s.Name), statusIcon(s.Status), statusFa(s.Status), s.PublicIP(), ipv4Note(&s), html.EscapeString(prov), s.Region,
		orDash(s.PanelVersion), orDash(s.XrayVersion), orDash(s.SyncStatus), check, s.RotateCount, traffic, s.OnlineCount)
	m.send(ctx, b, chat, text, [][]tgm.InlineKeyboardButton{
		{{Text: "🔄 تعویض IP", CallbackData: fmt.Sprintf("rot:%d", id)}, {Text: "📡 چک ایران", CallbackData: fmt.Sprintf("chk:%d", id)}},
		{{Text: "🔁 همگام‌سازی", CallbackData: fmt.Sprintf("syn:%d", id)}, {Text: "🔗 لینک‌ها", CallbackData: fmt.Sprintf("lnk:%d", id)}},
		{{Text: "📷 QR کانفیگ‌ها", CallbackData: fmt.Sprintf("qr:%d", id)}, {Text: "⬆️ بارگذاری", CallbackData: fmt.Sprintf("upl:%d", id)}},
		{{Text: "♻️ جایگزینی سرور", CallbackData: fmt.Sprintf("rpl:%d", id)}, {Text: "🗑 حذف", CallbackData: fmt.Sprintf("del:%d", id)}},
	})
}

func (m *Manager) sendLinks(ctx context.Context, b *tg.Bot, chat int64, id uint) {
	var s models.Server
	if m.DB.First(&s, id).Error != nil {
		return
	}
	snap, err := orchestrator.LoadSnapshot(m.DB)
	if err != nil {
		return
	}
	links, _ := orchestrator.Links(snap.For(&s), orchestrator.LinkHost(&s))
	var clean []linkgen.Link
	if m.Orch != nil {
		clean, _ = m.Orch.ServerCleanLinks(snap, &s)
	}
	if len(links) == 0 && len(clean) == 0 {
		m.send(ctx, b, chat, "لینکی وجود ندارد", nil)
		return
	}
	var sb strings.Builder
	write := func(line string) {
		if sb.Len()+len(line) > 3800 {
			m.send(ctx, b, chat, sb.String(), nil)
			sb.Reset()
		}
		sb.WriteString(line)
	}
	for _, l := range links {
		write("<code>" + html.EscapeString(l.URL) + "</code>\n\n")
	}
	if len(clean) > 0 {
		write("🌐 <b>لینک‌های IP تمیز کلودفلر</b>\n\n")
		for _, l := range clean {
			write("<code>" + html.EscapeString(l.URL) + "</code>\n\n")
		}
	}
	m.send(ctx, b, chat, sb.String(), nil)
}

// sendQRs sends each config of the server as a QR photo, captioned with its link.
func (m *Manager) sendQRs(ctx context.Context, b *tg.Bot, chat int64, id uint) {
	var s models.Server
	if m.DB.First(&s, id).Error != nil {
		return
	}
	snap, err := orchestrator.LoadSnapshot(m.DB)
	if err != nil {
		return
	}
	links, _ := orchestrator.Links(snap.For(&s), orchestrator.LinkHost(&s))
	if len(links) == 0 {
		m.send(ctx, b, chat, "لینکی وجود ندارد", nil)
		return
	}
	for _, l := range links {
		png, err := qrcode.Encode(l.URL, qrcode.Medium, 512)
		if err != nil {
			log.Printf("telegram qr: %v", err)
			continue
		}
		// Captions are capped at 1024 characters; drop the link if it does not fit.
		caption := "<b>" + html.EscapeString(l.Email) + "</b>\n<code>" + html.EscapeString(l.URL) + "</code>"
		if len([]rune(caption)) > 1000 {
			caption = "<b>" + html.EscapeString(l.Email) + "</b>"
		}
		if _, err := b.SendPhoto(ctx, &tg.SendPhotoParams{
			ChatID: chat, Photo: &tgm.InputFileUpload{Filename: "qr.png", Data: bytes.NewReader(png)},
			Caption: caption, ParseMode: tgm.ParseModeHTML,
		}); err != nil {
			log.Printf("telegram send qr: %v", err)
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func statusIcon(s string) string {
	switch s {
	case models.ServerActive:
		return "🟢"
	case models.ServerFiltered:
		return "🔴"
	case models.ServerError:
		return "⚠️"
	case models.ServerRotating:
		return "🔄"
	default:
		return "🟡"
	}
}

func statusFa(s string) string {
	return map[string]string{
		models.ServerCreating: "در حال ساخت", models.ServerChecking: "در حال چک", models.ServerInstalling: "نصب پنل",
		models.ServerConfiguring: "پیکربندی", models.ServerActive: "فعال", models.ServerFiltered: "فیلتر",
		models.ServerRotating: "تعویض IP", models.ServerError: "خطا", models.ServerDeleting: "در حال حذف", models.ServerDeleted: "حذف شده",
	}[s]
}

func kindFa(k string) string {
	if v, ok := map[string]string{
		orchestrator.KindProvision: "ساخت سرور", orchestrator.KindRotate: "تعویض IP", orchestrator.KindCheck: "چک",
		orchestrator.KindInstall: "نصب", orchestrator.KindSync: "همگام‌سازی", orchestrator.KindUpload: "بارگذاری",
		orchestrator.KindDelete: "حذف", orchestrator.KindXrayVersion: "تغییر هسته", orchestrator.KindPanelVersion: "تغییر نسخه پنل",
		orchestrator.KindProvisionAuto: "ساخت خودکار",
	}[k]; ok {
		return v
	}
	return k
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ipv4Note shows an IPv6 server's IPv4 (used for SSH) under its address.
func ipv4Note(s *models.Server) string {
	if s.Family() != models.FamilyV6 {
		return ""
	}
	return fmt.Sprintf(" (IPv6)\nIPv4: <code>%s</code>", s.IPv4)
}
