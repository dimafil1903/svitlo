package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"os/exec"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type DtekClient struct {
	city   string
	street string
	house  string

	mu          sync.Mutex
	cachedAt    time.Time
	cachedValue *DtekShutdown
	cacheHit    bool
}

type DtekShutdown struct {
	SubType   string   `json:"sub_type"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
	Type      string   `json:"type"`
	Reason    []string `json:"sub_type_reason"`
}

type DtekResponse struct {
	Result bool                    `json:"result"`
	Data   map[string]DtekShutdown `json:"data"`
}

func NewDtekClient(city, street, house string) *DtekClient {
	return &DtekClient{city: city, street: street, house: house}
}

func lookupBrowser() string {
	// rod's built-in search
	if path, has := launcher.LookPath(); has {
		return path
	}
	// snap and other common locations on Linux
	candidates := []string{
		"/snap/bin/chromium",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
	}
	for _, p := range candidates {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

func (d *DtekClient) FetchShutdowns() (*DtekShutdown, error) {
	browserPath := lookupBrowser()
	if browserPath == "" {
		return nil, fmt.Errorf("chromium not found; install it: snap install chromium")
	}
	log.Printf("[dtek] Using browser: %s", browserPath)

	u, err := launcher.New().
		Bin(browserPath).
		Headless(true).
		Set("no-sandbox").
		Set("disable-gpu").
		Launch()
	if err != nil {
		return nil, fmt.Errorf("launcher: %w", err)
	}

	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("browser connect: %w", err)
	}
	defer browser.MustClose()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "https://www.dtek-dnem.com.ua/ua/shutdowns"})
	if err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}

	// Wait for Imperva challenge
	page.WaitLoad()
	time.Sleep(5 * time.Second)

	// Get cookies
	cookies, err := page.Cookies([]string{"https://www.dtek-dnem.com.ua"})
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}

	// Get CSRF token
	csrfEl, err := page.Element(`meta[name="csrf-token"]`)
	if err != nil {
		return nil, fmt.Errorf("csrf element: %w", err)
	}
	csrfToken, err := csrfEl.Attribute("content")
	if err != nil || csrfToken == nil {
		return nil, fmt.Errorf("csrf attribute: %w", err)
	}

	log.Printf("[dtek] Got %d cookies, CSRF: %.20s", len(cookies), *csrfToken)

	var cookieParts []string
	for _, c := range cookies {
		cookieParts = append(cookieParts, c.Name+"="+c.Value)
	}
	cookieStr := strings.Join(cookieParts, "; ")

	now := time.Now().Format("02.01.2006 15:04")
	formData := url.Values{
		"method":         {"getHomeNum"},
		"data[0][name]":  {"city"},
		"data[0][value]": {d.city},
		"data[1][name]":  {"street"},
		"data[1][value]": {d.street},
		"data[2][name]":  {"updateFact"},
		"data[2][value]": {now},
	}

	req, err := http.NewRequest("POST", "https://www.dtek-dnem.com.ua/ua/ajax",
		strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CSRF-Token", *csrfToken)
	req.Header.Set("Referer", "https://www.dtek-dnem.com.ua/ua/shutdowns")
	req.Header.Set("Origin", "https://www.dtek-dnem.com.ua")
	req.Header.Set("Cookie", cookieStr)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("[dtek] Response status: %d, body: %.200s", resp.StatusCode, body)

	var dtekResp DtekResponse
	if err := json.Unmarshal(body, &dtekResp); err != nil {
		return nil, fmt.Errorf("parse response: %w, body: %s", err, body[:min(200, len(body))])
	}

	if !dtekResp.Result {
		return nil, fmt.Errorf("dtek returned result=false")
	}

	shutdown, ok := dtekResp.Data[d.house]
	if !ok {
		return nil, nil
	}

	return &shutdown, nil
}

const dtekCacheTTL = 10 * time.Minute

func (d *DtekClient) ClearCache() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cacheHit = false
	log.Printf("[dtek] Cache cleared")
}

func (d *DtekClient) GetShutdown() (*DtekShutdown, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cacheHit && time.Since(d.cachedAt) < dtekCacheTTL {
		return d.cachedValue, nil
	}

	shutdown, err := d.FetchShutdowns()
	if err != nil {
		return nil, err
	}

	d.cachedAt = time.Now()
	d.cachedValue = shutdown
	d.cacheHit = true
	return shutdown, nil
}

func (d *DtekClient) ShutdownLine() string {
	shutdown, err := d.GetShutdown()
	if err != nil {
		log.Printf("[dtek] error: %v", err)
		return "📋 ДТЕК: помилка отримання даних"
	}
	if shutdown == nil {
		return "📋 ДТЕК: відключень немає"
	}
	return fmt.Sprintf("📋 ДТЕК: %s – %s", shutdown.StartDate, shutdown.EndDate)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
