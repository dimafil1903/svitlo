package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type DeyeClient struct {
	baseURL   string
	appID     string
	appSecret string
	email     string
	password  string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
	httpClient  *http.Client
}

func NewDeyeClient(cfg *Config) *DeyeClient {
	return &DeyeClient{
		baseURL:   cfg.DeyeBaseURL,
		appID:     cfg.DeyeAppID,
		appSecret: cfg.DeyeAppSecret,
		email:     cfg.DeyeEmail,
		password:  cfg.DeyePassword,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// --- Auth ---

type tokenRequest struct {
	AppSecret string `json:"appSecret"`
	Email     string `json:"email"`
	Password  string `json:"password"`
}

type tokenResponse struct {
	Success      bool   `json:"success"`
	Code         string `json:"code"`
	Msg          string `json:"msg"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    string `json:"expiresIn"`
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:])
}

func (c *DeyeClient) Authenticate() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	body := tokenRequest{
		AppSecret: c.appSecret,
		Email:     c.email,
		Password:  sha256Hex(c.password),
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal token request: %w", err)
	}

	url := fmt.Sprintf("%s/v1.0/account/token?appId=%s", c.baseURL, c.appID)
	log.Printf("[deye] >>> POST %s", url)
	log.Printf("[deye] >>> Body: %s", string(data))

	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read token response: %w", err)
	}

	log.Printf("[deye] <<< %d %s", resp.StatusCode, string(respBody))

	var tokenResp tokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return fmt.Errorf("unmarshal token response: %w", err)
	}

	if !tokenResp.Success {
		return fmt.Errorf("deye auth failed: code=%s msg=%s", tokenResp.Code, tokenResp.Msg)
	}

	// Ensure token has "Bearer " prefix
	token := tokenResp.AccessToken
	if !strings.HasPrefix(token, "Bearer ") {
		token = "Bearer " + token
	}
	c.accessToken = token
	// Token expires in ~60 days, refresh 1 hour before
	c.expiresAt = time.Now().Add(59 * 24 * time.Hour)

	log.Printf("[deye] Auth OK, token: %s...%s, expires: %s",
		c.accessToken[:15], c.accessToken[len(c.accessToken)-6:],
		c.expiresAt.Format("2006-01-02 15:04"))

	return nil
}

func (c *DeyeClient) getToken() (string, error) {
	c.mu.Lock()
	token := c.accessToken
	expired := time.Now().After(c.expiresAt)
	c.mu.Unlock()

	if token == "" || expired {
		if err := c.Authenticate(); err != nil {
			return "", err
		}
		c.mu.Lock()
		token = c.accessToken
		c.mu.Unlock()
	}
	return token, nil
}

func (c *DeyeClient) doRequest(path string, reqBody interface{}, result interface{}) error {
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + path
	log.Printf("[deye] >>> POST %s", url)
	log.Printf("[deye] >>> Body: %s", string(data))
	log.Printf("[deye] >>> Authorization: %s...%s", token[:15], token[len(token)-6:])

	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	log.Printf("[deye] <<< %d %s", resp.StatusCode, string(respBody))

	// If unauthorized, try re-auth once
	if resp.StatusCode == 401 {
		log.Printf("[deye] Got 401, re-authenticating...")
		if err := c.Authenticate(); err != nil {
			return fmt.Errorf("re-auth failed: %w", err)
		}
		return c.doRequest(path, reqBody, result)
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBody))
	}

	return nil
}

// --- Device List (discovery) ---

type DeviceListRequest struct {
	Page int `json:"page"`
	Size int `json:"size"`
}

type DeviceListItem struct {
	DeviceSn      string `json:"deviceSn"`
	DeviceID      int64  `json:"deviceId"`
	DeviceType    string `json:"deviceType"`
	ProductID     int64  `json:"productId"`
	StationID     int64  `json:"stationId"`
	ConnectStatus int    `json:"connectStatus"`
	ProductName   string `json:"productName"`
	StationName   string `json:"stationName"`
}

type DeviceListResponse struct {
	Success bool             `json:"success"`
	Code    string           `json:"code"`
	Msg     string           `json:"msg"`
	Total   int              `json:"total"`
	Devices []DeviceListItem `json:"deviceListItems"`
}

func (c *DeyeClient) GetDeviceList() (*DeviceListResponse, error) {
	reqBody := DeviceListRequest{Page: 1, Size: 100}
	var resp DeviceListResponse
	if err := c.doRequest("/v1.0/device/list", reqBody, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("device/list failed: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

// --- Station Latest ---

type StationLatestRequest struct {
	StationID int64 `json:"stationId"`
}

type StationLatestResponse struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Msg     string `json:"msg"`

	GenerationPower  *float64 `json:"generationPower"`
	ConsumptionPower *float64 `json:"consumptionPower"`
	GridPower        *float64 `json:"gridPower"`
	PurchasePower    *float64 `json:"purchasePower"`
	WirePower        *float64 `json:"wirePower"`
	BatteryPower     *float64 `json:"batteryPower"`
	BatterySOC       *float64 `json:"batterySOC"`
	ChargePower      *float64 `json:"chargePower"`
	DischargePower   *float64 `json:"dischargePower"`
	LastUpdateTime   float64  `json:"lastUpdateTime"`
}

func (c *DeyeClient) GetStationLatest(stationID int64) (*StationLatestResponse, error) {
	reqBody := StationLatestRequest{StationID: stationID}
	var resp StationLatestResponse
	if err := c.doRequest("/v1.0/station/latest", reqBody, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("station/latest failed: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

// --- Device Latest ---

type DeviceLatestRequest struct {
	DeviceList []string `json:"deviceList"`
}

type DeviceDataItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type DeviceLatestEntry struct {
	DeviceSn       string           `json:"deviceSn"`
	DeviceState    int              `json:"deviceState"` // 1=Online, 2=Alert, 3=Offline
	CollectionTime int64            `json:"collectionTime"`
	DataList       []DeviceDataItem `json:"dataList"`
}

type DeviceLatestResponse struct {
	Success    bool                `json:"success"`
	Code       string              `json:"code"`
	Msg        string              `json:"msg"`
	DeviceList []DeviceLatestEntry `json:"deviceListItems"`
}

func (c *DeyeClient) GetDeviceLatest(deviceSNs []string) (*DeviceLatestResponse, error) {
	reqBody := DeviceLatestRequest{DeviceList: deviceSNs}
	var resp DeviceLatestResponse
	if err := c.doRequest("/v1.0/device/latest", reqBody, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("device/latest failed: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return &resp, nil
}

// --- Power Status ---

type PowerStatus struct {
	HasGrid          bool
	GridPower        float64
	PurchasePower    float64
	GenerationPower  float64
	ConsumptionPower float64
	BatterySOC       float64
	BatteryPower     float64
	DischargePower   float64
	DeviceOnline     bool
	DeviceState      int
	LastUpdateTime   float64 // unix timestamp
}

func ptrVal(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func (c *DeyeClient) GetPowerStatus(stationID int64, deviceSN string) (*PowerStatus, error) {
	station, err := c.GetStationLatest(stationID)
	if err != nil {
		return nil, fmt.Errorf("get station: %w", err)
	}

	device, err := c.GetDeviceLatest([]string{deviceSN})
	if err != nil {
		return nil, fmt.Errorf("get device: %w", err)
	}

	gridPower := ptrVal(station.GridPower)
	purchasePower := ptrVal(station.PurchasePower)

	// Determine if grid is available:
	// - If gridPower or purchasePower are non-null and > 0 → grid is ON
	// - If both are null → check dischargePower: if battery discharges, grid is likely OFF
	hasGrid := false
	if station.GridPower != nil || station.PurchasePower != nil {
		hasGrid = gridPower > 0 || purchasePower > 0
	}

	status := &PowerStatus{
		HasGrid:          hasGrid,
		GridPower:        gridPower,
		PurchasePower:    purchasePower,
		GenerationPower:  ptrVal(station.GenerationPower),
		ConsumptionPower: ptrVal(station.ConsumptionPower),
		BatterySOC:       ptrVal(station.BatterySOC),
		BatteryPower:     ptrVal(station.BatteryPower),
		DischargePower:   ptrVal(station.DischargePower),
		LastUpdateTime:   station.LastUpdateTime,
	}

	if len(device.DeviceList) > 0 {
		status.DeviceOnline = device.DeviceList[0].DeviceState == 1
		status.DeviceState = device.DeviceList[0].DeviceState
	}

	return status, nil
}
