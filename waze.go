//go:generate encoding=UTF-8

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

	"github.com/patrickmn/go-cache"
)

type Filters struct {
	ChitChat bool `json:"chitChat"`
	Police   bool `json:"police"`
	Jam      bool `json:"jam"`
	Accident bool `json:"accident"`
	Unknown  bool `json:"unknown"`
}

func loadFilters(filename string) *Filters {
	file, err := os.Open(filename)
	if err != nil {
		log.Printf("Erro ao abrir arquivo JSON de filtros: %v", err)
		return &Filters{}
	}
	defer file.Close()

	var filters Filters
	if err := json.NewDecoder(file).Decode(&filters); err != nil {
		log.Printf("Erro ao abrir o arquivo JSON de filtros: %v", err)
		return &Filters{}
	}

	return &filters
}

func saveFilters(filename string, filters *Filters) {
	file, err := os.Create(filename)
	if err != nil {
		log.Printf("Erro ao criar arquivo JSON de filtros: %v", err)
		return
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(filters); err != nil {
		log.Printf("Erro ao codificar arquivo JSON de filtros: %v", err)
		return
	}
}

var (
	telegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	telegramChatID   = os.Getenv("TELEGRAM_CHAT_ID")

	db              = NewDatabase("db.json")
	processedAlerts = db.GetProcessedAlerts()
	maxWazersOnline = db.GetMaxWazersOnline()
	c               *cache.Cache

	options = struct {
		areaBounds       map[string]float64
		requestURL       string
		broadcastFeedURL string
	}{
		areaBounds: map[string]float64{
			"left":   -52.2100,
			"right":  -48.5400,
			"top":    -26.5000,
			"bottom": -27.5000,
		},
		requestURL:       "https://www.waze.com/row-rtserver/web/TGeoRSS?tk=community&format=JSON",
		broadcastFeedURL: "https://www.waze.com/row-rtserver/broadcast/BroadcastRSS?buid=22c8ece8ae5b984902e7d1c69f5db4bf&format=JSON",
	}

	alerts       []map[string]interface{}
	alertsLock   sync.Mutex
	alertsCh     = make(chan map[string]interface{}, 10)
	clients      = make(map[chan struct{}]struct{})
	clientsLock  sync.Mutex
	wg           sync.WaitGroup
	shutdownOnce sync.Once
	filters      *Filters
	filtersLock  sync.Mutex
)

func main() {
	c = cache.New(5*time.Minute, 10*time.Minute)
	filters = loadFilters("filters.json")
	wg.Add(1)
	go startWebServer()
	go scheduleJob("*/30 * * * * *", getUpdates)
	go scheduleJob("*/20 * * * * *", countWazers)
	go scheduleJob("0 * * * *", sendWazersReport)

	go func() {
		wg.Wait()
		close(alertsCh)
	}()

	for alert := range alertsCh {
		alertsLock.Lock()
		alerts = append(alerts, alert)
		alertsLock.Unlock()

		clientsLock.Lock()
		for client := range clients {
			client <- struct{}{}
		}
		clientsLock.Unlock()
	}
}

func startWebServer() {
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/alerts", handleAlerts)
	http.HandleFunc("/events", handleEvents)
	http.HandleFunc("/filters", handleFilters)
	http.HandleFunc("/updateFilters", handleUpdateFilters)
	log.Fatal(http.ListenAndServe(":9091", nil))
}

func handleUpdateFilters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var newFilters Filters
	if err := json.NewDecoder(r.Body).Decode(&newFilters); err != nil {
		http.Error(w, "Erro ao decodificar filtros", http.StatusBadRequest)
		return
	}

	filtersLock.Lock()
	filters = &newFilters
	saveFilters("filters.json", filters)
	filtersLock.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "Bem-vindo ao servidor de alertas do Waze\n\n")
	fmt.Fprintf(w, "Para ver os alertas, acesse /alerts\n")
	fmt.Fprintf(w, "Para receber os alertas em tempo real, acesse /events\n")
	fmt.Fprintf(w, "Para configurar os filtros, acesse /filters\n")
}

func handleAlerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	alertsLock.Lock()
	defer alertsLock.Unlock()
	json.NewEncoder(w).Encode(alerts)
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	notify := r.Context().Done()
	client := make(chan struct{}, 1)

	clientsLock.Lock()
	clients[client] = struct{}{}
	clientsLock.Unlock()

	defer func() {
		clientsLock.Lock()
		delete(clients, client)
		clientsLock.Unlock()
		close(client)
	}()

	for {
		select {
		case <-notify:
			logger("Cliente desconectado")
			return
		case <-client:
			logger("Enviando eventos para o cliente")
			alertsLock.Lock()
			for _, alert := range alerts {
				eventType := alert["type"].(string)
				var message string

				switch eventType {
				case "CHIT_CHAT":
					if filters.ChitChat {
						message = handleChitChat(alert)
					}
				case "POLICE", "POLICEMAN":
					if filters.Police {
						message = handlePoliceAlert(alert)
					}
				case "JAM":
					if filters.Jam {
						message = handleJamAlert(alert)
					}
				case "ACCIDENT":
					if filters.Accident {
						message = handleAccidentAlert(alert)
					}
				default:
					if filters.Unknown {
						message = handleUnknownAlert(alert)
					}
				}

				if message != "" {
					fmt.Fprintf(w, "data: %s\n\n", message)
					w.(http.Flusher).Flush()
					logger("Evento enviado")
				}
			}
			alertsLock.Unlock()
		}
	}
}

func handleFilters(w http.ResponseWriter, r *http.Request) {
	html := `
	<!DOCTYPE html>
	<html>
	<head>
		<title>Configurar Filtros</title>
	</head>
	<body>
		<h1>Configurar Filtros</h1>
		<form id="filterForm">
			<label><input type="checkbox" name="chit_chat"> Comnetário</label><br>
			<label><input type="checkbox" name="police"> Polícia</label><br>
			<label><input type="checkbox" name="jam"> Congestionamento</label><br>
			<label><input type="checkbox" name="accident"> Acidente</label><br>
			<label><input type="checkbox" name="unknown"> Outros</label><br>
			<button type="submit">Salvar</button>
		</form>
		<script>
			document.getElementById('filterForm').addEventListener('submit', function(event) {
				event.preventDefault();
				const formData = new FormData(this);
				const filters = {};
				for (const [name, value] of formData.entries()) {
					filters[name] = value === 'on';
				}
				fetch('/updateFilters', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/json',
					},
					body: JSON.stringify(filters),
				}).then(() => {
					alert('Filtros atualizados com sucesso');
				}).catch((error) => {
					alert('Erro ao atualizar filtros');
					console.error(error);
				});
			});
		</script>
	</body>
	</html>
	`
	fmt.Fprintf(w, html)
}

func handleChitChat(alert map[string]interface{}) string {
	reportBy := alert["reportBy"].(string)
	location := alert["location"].(string)

	return fmt.Sprintf("[%s] 📢 %s deixou um comentário no mapa 💭\nAnálise 🗺️: %s", time.Now().Format("15:04:05"), reportBy, location)
}

func handlePoliceAlert(alert map[string]interface{}) string {
	info := formatAlertData(alert)
	return fmt.Sprintf("[%s] 📢 Polícia &#128660;\n```%s```", time.Now().Format("15:04:05"), info)
}

func handleJamAlert(alert map[string]interface{}) string {
	info := formatAlertData(alert)
	return fmt.Sprintf("[%s] 📢 Congestionamento 🚗🚕🚙\n```%s```", time.Now().Format("15:04:05"), info)
}

func handleAccidentAlert(alert map[string]interface{}) string {
	info := formatAlertData(alert)
	return fmt.Sprintf("[%s] 📢 Acidente 🚙💥🚕\n```%s```", time.Now().Format("15:04:05"), info)
}

func handleUnknownAlert(alert map[string]interface{}) string {
	info := formatAlertData(alert)
	return fmt.Sprintf("[%s] 🤖 Tipo de notificação desconhecida\n```%s```", time.Now().Format("15:04:05"), info)
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

	// Verifica se os dados estão no cache
	if data, found := c.Get("wazeData"); found {
		processAlerts(data.([]interface{}))
		return
	}

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

	// Adiciona os dados ao cache
	c.Set("wazeData", data["alerts"].([]interface{}), cache.DefaultExpiration)

	processAlerts(data["alerts"].([]interface{}))
}

func processAlerts(alerts []interface{}) {
	logger("processando alertas")

	for _, alert := range alerts {
		alertData := alert.(map[string]interface{})
		alertID := alertData["uuid"].(string)
		if !processedAlerts.Has(alertID) {
			alertsCh <- alertData
			processedAlerts.Add(alertID)
		}
	}
}

func countWazers() {
	logger("contando motoristas")

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
