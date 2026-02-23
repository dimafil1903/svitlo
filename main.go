package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	deye := NewDeyeClient(cfg)
	bot := NewTelegramBot(cfg.TelegramBotToken, cfg.TelegramUserIDs)
	dtek := NewDtekClient("м. Підгороднє", "вул. Сагайдачного Петра", "63")

	log.Println("Authenticating with Deye Cloud...")
	if err := deye.Authenticate(); err != nil {
		log.Fatalf("Deye authentication failed: %v", err)
	}
	log.Println("Deye authentication successful")

	// Auto-discover station ID and device SN if not set
	if cfg.DeyeStationID == 0 || cfg.DeyeDeviceSN == "" {
		log.Println("DEYE_STATION_ID or DEYE_DEVICE_SN not set, discovering devices...")
		devices, err := deye.GetDeviceList()
		if err != nil {
			log.Fatalf("Failed to get device list: %v", err)
		}
		if len(devices.Devices) == 0 {
			log.Fatal("No devices found on your Deye account")
		}
		log.Printf("Found %d device(s):", len(devices.Devices))
		for i, d := range devices.Devices {
			log.Printf("  [%d] SN: %s | StationID: %d | Type: %s | Name: %s | Station: %s | Status: %d",
				i, d.DeviceSn, d.StationID, d.DeviceType, d.ProductName, d.StationName, d.ConnectStatus)
		}
		// Use first device
		first := devices.Devices[0]
		if cfg.DeyeStationID == 0 {
			cfg.DeyeStationID = first.StationID
			log.Printf("Using StationID: %d (set DEYE_STATION_ID=%d in .env to skip discovery)", first.StationID, first.StationID)
		}
		if cfg.DeyeDeviceSN == "" {
			cfg.DeyeDeviceSN = first.DeviceSn
			log.Printf("Using DeviceSN: %s (set DEYE_DEVICE_SN=%s in .env to skip discovery)", first.DeviceSn, first.DeviceSn)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Deye polling goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDeyePoller(ctx, deye, bot, cfg, dtek)
	}()

	// Telegram updates goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		runTelegramPoller(ctx, deye, bot, cfg, dtek)
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down...", sig)
	cancel()
	wg.Wait()
	log.Println("Shutdown complete")
}

func runDeyePoller(ctx context.Context, deye *DeyeClient, bot *TelegramBot, cfg *Config, dtek *DtekClient) {
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalSec) * time.Second)
	defer ticker.Stop()

	var lastHasGrid *bool

	checkAndNotify := func() {
		status, err := deye.GetPowerStatus(cfg.DeyeStationID, cfg.DeyeDeviceSN)
		if err != nil {
			log.Printf("[deye] Failed to get power status: %v", err)
			return
		}

		log.Printf("[deye] Grid: %.0fW | Purchase: %.0fW | Gen: %.0fW | Cons: %.0fW | SOC: %.0f%% | Online: %v",
			status.GridPower, status.PurchasePower,
			status.GenerationPower, status.ConsumptionPower,
			status.BatterySOC, status.DeviceOnline)

		currentHasGrid := status.HasGrid

		if lastHasGrid == nil {
			// First check — save state, send current status
			lastHasGrid = &currentHasGrid
			msg := formatStatusMessage(status, dtek.ShutdownLine())
			bot.Broadcast(msg)
			log.Printf("[deye] Initial state: hasGrid=%v", currentHasGrid)
			return
		}

		if currentHasGrid != *lastHasGrid {
			// State changed!
			*lastHasGrid = currentHasGrid
			var msg string
			if currentHasGrid {
				msg = formatPowerOnMessage(status, dtek.ShutdownLine())
			} else {
				msg = formatPowerOffMessage(status, dtek.ShutdownLine())
			}
			bot.Broadcast(msg)
			log.Printf("[deye] State changed: hasGrid=%v", currentHasGrid)
		}
	}

	// First check immediately
	checkAndNotify()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkAndNotify()
		}
	}
}

func runTelegramPoller(ctx context.Context, deye *DeyeClient, bot *TelegramBot, cfg *Config, dtek *DtekClient) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := bot.GetUpdates()
		if err != nil {
			log.Printf("[telegram] Failed to get updates: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.Message == nil {
				continue
			}

			chatID := update.Message.Chat.ID

			if !bot.IsAllowedUser(chatID) {
				log.Printf("[telegram] Unauthorized user: %d", chatID)
				continue
			}

			switch update.Message.Text {
			case "/status":
				handleStatusCommand(deye, bot, cfg, chatID, dtek)
			case "/start":
				if err := bot.SendMessage(chatID, "Бот Світло активний. Використовуй /status щоб перевірити стан електрики."); err != nil {
					log.Printf("[telegram] Failed to send /start reply: %v", err)
				}
			}
		}
	}
}

func handleStatusCommand(deye *DeyeClient, bot *TelegramBot, cfg *Config, chatID int64, dtek *DtekClient) {
	status, err := deye.GetPowerStatus(cfg.DeyeStationID, cfg.DeyeDeviceSN)
	if err != nil {
		log.Printf("[telegram] Failed to get status for /status command: %v", err)
		if sendErr := bot.SendMessage(chatID, "Помилка при отриманні статусу. Спробуйте пізніше."); sendErr != nil {
			log.Printf("[telegram] Failed to send error message: %v", sendErr)
		}
		return
	}

	msg := formatStatusMessage(status, dtek.ShutdownLine())
	if err := bot.SendMessage(chatID, msg); err != nil {
		log.Printf("[telegram] Failed to send status: %v", err)
	}
}

func formatPowerOnMessage(s *PowerStatus, dtekLine string) string {
	return fmt.Sprintf(
		"<b>⚡ Світло З'ЯВИЛОСЬ!</b>\n\n"+
			"🔌 Мережа: %.0fW\n"+
			"🔋 Батарея: %.0f%%\n"+
			"☀️ Генерація: %.0fW\n"+
			"🏠 Споживання: %.0fW\n"+
			"%s\n"+
			"🕐 %s",
		s.GridPower, s.BatterySOC,
		s.GenerationPower, s.ConsumptionPower,
		dtekLine,
		formatTime(s.LastUpdateTime),
	)
}

func formatPowerOffMessage(s *PowerStatus, dtekLine string) string {
	return fmt.Sprintf(
		"<b>❌ Світло ЗНИКЛО!</b>\n\n"+
			"🔋 Батарея: %.0f%%\n"+
			"☀️ Генерація: %.0fW\n"+
			"🏠 Споживання: %.0fW\n"+
			"%s\n"+
			"🕐 %s",
		s.BatterySOC,
		s.GenerationPower, s.ConsumptionPower,
		dtekLine,
		formatTime(s.LastUpdateTime),
	)
}

func formatStatusMessage(s *PowerStatus, dtekLine string) string {
	gridStatus := "❌ Світла НЕМАЄ, але є добро"
	if s.HasGrid {
		gridStatus = "⚡ Світло Є, але нема добра((("
	}

	deviceStatus := "Офлайн"
	switch s.DeviceState {
	case 1:
		deviceStatus = "Онлайн"
	case 2:
		deviceStatus = "Тривога"
	case 3:
		deviceStatus = "Офлайн"
	}

	batteryLine := fmt.Sprintf("🔋 Батарея: %.0f%% (%.0fW)", s.BatterySOC, s.BatteryPower)
	if s.BatteryTemp != nil {
		batteryLine += fmt.Sprintf(" %.0f°C", *s.BatteryTemp)
	}

	return fmt.Sprintf(
		"<b>%s</b>\n\n"+
			"☀️ Генерація: %.0fW\n"+
			"🏠 Споживання: %.0fW\n"+
			"%s\n"+
			"📡 Пристрій: %s\n"+
			"%s\n"+
			"🕐 %s",
		gridStatus,
		s.GenerationPower, s.ConsumptionPower,
		batteryLine,
		deviceStatus,
		dtekLine,
		formatTime(s.LastUpdateTime),
	)
}

func formatTime(ts float64) string {
	if ts == 0 {
		return time.Now().Format("15:04 02.01.2006")
	}
	t := time.Unix(int64(ts), 0)
	return t.Format("15:04 02.01.2006")
}
