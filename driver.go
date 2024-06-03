package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	telegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	telegramChatID   = os.Getenv("TELEGRAM_CHAT_ID")

	db              = NewDatabase("db.json")
	processedAlerts = db.GetProcessedAlerts()
	maxWazersOnline = db.GetMaxWazersOnline()

	options = struct {
		areaBounds       map[string]float64
		requestURL       string
		broadcastFeedURL string
	}{
		areaBounds: map[string]float64{
			"left":   -49.640,
			"right":  -49.230,
			"top":    -27.150,
			"bottom": -27.800,
		},
		requestURL:       "https://www.waze.com/row-rtserver/web/TGeoRSS?tk=community&format=JSON",
		broadcastFeedURL: "https://www.waze.com/row-rtserver/broadcast/BroadcastRSS?buid=xxxxxxxxxxxxxxxxxxxxxxx&format=JSON",
	}

	wg sync.WaitGroup
)

func main() {
	wg.Add(1)
	go scheduleJob("*/30 * * * * *", getUpdates)
	go scheduleJob("*/20 * * * * *", countWazers)
	go scheduleJob("0 * * * *", sendWazersReport)

	wg.Wait()
}

func scheduleJob(cron string, job func()) {
	defer wg.Done()

	for {
		now := time.Now()
		next := now.Add(1 * time.Minute)
		next = time.Date(next.Year(), next.Month(), next.Day(), next.Hour(), next.Minute(), 0, 0, next.Location())

		timer := time.NewTimer(next.Sub(now))
		<-timer.C

		job()
	}
}

func getUpdates() {
	logger("getting updates")

	url := addBoundsToURL(options.areaBounds, options.requestURL)

	resp, err := http.Get(url)
	if err != nil {
		logger("ERROR: can't get updates")
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		logger("ERROR: can't decode response")
		return
	}

	if _, ok := data["alerts"]; !ok {
		logger("ERROR: 'alerts' key not found in data")
		return
	}

	processAlerts(data["alerts"].([]interface{}))
}

func processData(data map[string]interface{}) {
	if alertsData, ok := data["alerts"]; ok {
		if alerts, ok := alertsData.([]interface{}); ok {
			processAlerts(alerts)
			return
		}
	}

	logger("ERROR: 'alerts' key not found or is not an array in data")
}

func processAlerts(alerts []interface{}) {
	logger("processando alertas")

	for _, alert := range alerts {
		alertID := alert.(map[string]interface{})["uuid"].(string)
		if !processedAlerts.Has(alertID) {
			go handleAlert(alert)
			processedAlerts.Add(alertID)
		}
	}
}

func handleAlert(alert interface{}) {
	alertData := alert.(map[string]interface{})
	alertType := alertData["type"].(string)

	switch alertType {
	case "CHIT_CHAT":
		handleChitChat(alertData)
	case "POLICE", "POLICEMAN":
		handlePoliceAlert(alertData)
	case "JAM":
		handleJamAlert(alertData)
	case "ACCIDENT":
		handleAccidentAlert(alertData)
	default:
		handleUnknownAlert(alertData)
	}
}

func handleChitChat(alert map[string]interface{}) {
	reportBy := alert["reportBy"].(string)
	location := alert["location"].(string)

	message := fmt.Sprintf("📢 %s deixou um comentário no mapa 💭\nAnálise 🗺️: %s", reportBy, location)
	sendMessage(message)
	fmt.Println("ChitChat Alert:", message)
}

func handlePoliceAlert(alert map[string]interface{}) {
	sendMessage("📢 Polícia 🚓")
}

func handleJamAlert(alert map[string]interface{}) {
	message := "📢 Congestionamento 🚗🚕🚙"
	sendMessage(message)

	// Exibir alerta na tela
	fmt.Println("Jam Alert:", message)
}

func handleAccidentAlert(alert map[string]interface{}) {
	sendMessage("📢 Acidente 🚙💥🚕")
}

func handleUnknownAlert(alert map[string]interface{}) {
	info := formatAlertData(alert)
	message := fmt.Sprintf("🤖 Tipo de notificação desconhecida\n```%s```", info)
	sendMessage(message)
}

func countWazers() {
	logger("counting wazers")

	resp, err := http.Get(options.broadcastFeedURL)
	if err != nil {
		logger("ERROR: can't count wazers")
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		logger("ERROR: can't decode response")
		return
	}

	usersOnJams := data["usersOnJams"].([]interface{})
	actualWazersOnline := 0
	for _, jam := range usersOnJams {
		wazersCount := jam.(map[string]interface{})["wazersCount"].(float64)
		actualWazersOnline += int(wazersCount)
	}

	if actualWazersOnline > maxWazersOnline.Get() {
		maxWazersOnline.Set(actualWazersOnline)
	}
}

func sendWazersReport() {
	maxWazers := maxWazersOnline.Get()
	if maxWazers > 0 {
		message := fmt.Sprintf("%d wazers conectados 🚙 🚕 🚚", maxWazers)
		sendMessage(message)
		maxWazersOnline.Set(0)
	}
}

func addBoundsToURL(bounds map[string]float64, sourceURL string) string {
	var sb strings.Builder
	sb.WriteString(sourceURL)

	for key, val := range bounds {
		sb.WriteString(fmt.Sprintf("&%s=%.4f", key, val))
	}

	return sb.String()
}

func sendMessage(text string) {
	fmt.Println(text)
}

func logger(msg string) {
	t := time.Now()
	fmt.Printf("[%02d:%02d:%02d] %s\n", t.Hour(), t.Minute(), t.Second(), msg)
}

func formatAlertData(alert map[string]interface{}) string {
	var sb strings.Builder

	for key, val := range alert {
		sb.WriteString(fmt.Sprintf("%s: %v\n", key, val))
	}

	return sb.String()
}

type Database struct {
	filename string
	data     map[string]interface{}
	mu       sync.Mutex
}

func NewDatabase(filename string) *Database {
	return &Database{filename: filename, data: make(map[string]interface{})}
}

func (db *Database) load() {
	file, err := os.Open(db.filename)
	if err != nil {
		log.Println("ERROR: can't open database file")
		return
	}
	defer file.Close()

	err = json.NewDecoder(file).Decode(&db.data)
	if err != nil {
		log.Println("ERROR: can't decode database file")
		return
	}
}

func (db *Database) save() {
	file, err := os.Create(db.filename)
	if err != nil {
		log.Println("ERROR: can't create database file")
		return
	}
	defer file.Close()

	err = json.NewEncoder(file).Encode(&db.data)
	if err != nil {
		log.Println("ERROR: can't encode database file")
		return
	}
}

func (db *Database) GetProcessedAlerts() *Set {
	db.load()
	alerts, ok := db.data["processedAlerts"].([]string)
	if !ok {
		alerts = []string{}
	}
	return NewSet(alerts)
}

func (db *Database) GetMaxWazersOnline() *Counter {
	db.load()
	count, ok := db.data["maxWazersOnline"].(int)
	if !ok {
		count = 0
	}
	return NewCounter(count)
}

func (db *Database) SetProcessedAlerts(alerts *Set) {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.data["processedAlerts"] = alerts.Slice()
	db.save()
}

func (db *Database) SetMaxWazersOnline(count *Counter) {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.data["maxWazersOnline"] = count.Get()
	db.save()
}

type Set struct {
	data map[string]struct{}
	mu   sync.Mutex
}

func NewSet(items []string) *Set {
	set := &Set{data: make(map[string]struct{})}
	for _, item := range items {
		set.Add(item)
	}
	return set
}

func (s *Set) Add(item string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[item] = struct{}{}
}

func (s *Set) Remove(item string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data, item)
}

func (s *Set) Has(item string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.data[item]
	return ok
}

func (s *Set) Slice() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var items []string
	for item := range s.data {
		items = append(items, item)
	}
	return items
}

type Counter struct {
	count int
	mu    sync.Mutex
}

func NewCounter(count int) *Counter {
	return &Counter{count: count}
}

func (c *Counter) Get() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.count
}

func (c *Counter) Set(count int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.count = count
}
