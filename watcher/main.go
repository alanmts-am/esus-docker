package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/gorilla/websocket"
)

type QueryMessage struct {
	PID       string `json:"pid"`
	Query     string `json:"query"`
	Timestamp string `json:"timestamp"`
	Table     string `json:"table"`
}

type pendingQuery struct {
	SQL       string
	Timestamp string
	DisplayID string
}

var (
	reExecute = regexp.MustCompile(`(?i)^(?:(.*)\s+)?\[(\d+)\](?:\s+\[([^\]]*)\])?\s+LOG:\s+execute .*: (.*)`)
	reParams  = regexp.MustCompile(`(?i)^(?:(.*)\s+)?\[(\d+)\](?:\s+\[([^\]]*)\])?\s+DETAIL:\s+parameters?[:]?\s*(.*)`)

	queryState = make(map[string]pendingQuery)
	stateMu    sync.Mutex

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	clients   = make(map[*websocket.Conn]bool)
	clientsMu sync.Mutex
	broadcast = make(chan QueryMessage)

	watchID = os.Getenv("WATCH_ID")

	currentPID    string
	currentType   string
	pendingParams string

	db *sql.DB
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func initDB() {
	host := getEnv("PG_HOST", "db")
	port := getEnv("PG_PORT", "5432")
	user := getEnv("PG_USER", "postgres")
	password := getEnv("PG_PASSWORD", "esus")
	dbName := getEnv("PG_DBNAME", "watcher")

	// Connect to the default postgres database to create the watcher db if needed
	adminDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable", host, port, user, password)

	var adminDB *sql.DB
	var err error
	for i := range 10 {
		adminDB, err = sql.Open("postgres", adminDSN)
		if err == nil {
			err = adminDB.Ping()
		}
		if err == nil {
			break
		}
		log.Printf("Waiting for PostgreSQL (attempt %d/10): %v", i+1, err)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		log.Fatalf("Could not connect to PostgreSQL: %v", err)
	}

	_, err = adminDB.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		log.Fatalf("Failed to create database %q: %v", dbName, err)
	}
	adminDB.Close()

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, password, dbName)
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to open watcher database: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS queries (
		id         SERIAL PRIMARY KEY,
		pid        TEXT,
		query      TEXT,
		timestamp  TEXT,
		table_name TEXT,
		created_at TIMESTAMPTZ DEFAULT NOW()
	)`)
	if err != nil {
		log.Fatalf("Failed to create queries table: %v", err)
	}

	log.Printf("Database %q ready", dbName)
}

func saveQuery(msg QueryMessage) {
	_, err := db.Exec(
		`INSERT INTO queries (pid, query, timestamp, table_name) VALUES ($1, $2, $3, $4)`,
		msg.PID, msg.Query, msg.Timestamp, msg.Table,
	)
	if err != nil {
		log.Printf("Failed to save query: %v", err)
	}
}

func handleGetQueries(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT pid, query, timestamp, table_name FROM queries ORDER BY id DESC LIMIT 100`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	queries := []QueryMessage{}
	for rows.Next() {
		var q QueryMessage
		if err := rows.Scan(&q.PID, &q.Query, &q.Timestamp, &q.Table); err != nil {
			continue
		}
		queries = append(queries, q)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(queries)
}

func main() {
	initDB()

	containerName := getEnv("TARGET_CONTAINER", "esus_db_5_4_36_postgre18")
	fmt.Printf("Starting SQL Watcher monitoring container: %s via /var/run/docker.sock\n", containerName)

	go streamDockerLogs(containerName)
	go handleMessages()

	http.HandleFunc("/api/queries", handleGetQueries)
	http.HandleFunc("/ws", handleConnections)
	http.Handle("/", http.FileServer(http.Dir("./static")))

	port := "8081"
	fmt.Printf("Server starting on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func streamDockerLogs(containerName string) {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", "/var/run/docker.sock")
			},
		},
	}

	url := fmt.Sprintf("http://localhost/v1.41/containers/%s/logs?stdout=1&stderr=1&follow=1&tail=10", containerName)

	resp, err := client.Get(url)
	if err != nil {
		log.Printf("Error connecting to Docker Socket: %v. Retrying in 5s...", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Docker API error (%d): %s", resp.StatusCode, string(body))
		os.Exit(1)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		header := make([]byte, 8)
		_, err := io.ReadFull(reader, header)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Header read error: %v", err)
			break
		}

		size := uint32(header[4])<<24 | uint32(header[5])<<16 | uint32(header[6])<<8 | uint32(header[7])
		payload := make([]byte, size)
		_, err = io.ReadFull(reader, payload)
		if err != nil {
			log.Printf("Payload read error: %v", err)
			break
		}

		lines := strings.Split(string(payload), "\n")
		for _, l := range lines {
			if l != "" {
				processLine(l)
			}
		}
	}
}

func processLine(logText string) {
	stateMu.Lock()
	defer stateMu.Unlock()

	if (strings.HasPrefix(logText, "\t") || strings.HasPrefix(logText, " ")) && currentPID != "" {
		if currentType == "EXECUTE" {
			pending := queryState[currentPID]
			pending.SQL += " " + strings.TrimSpace(logText)
			queryState[currentPID] = pending
		} else if currentType == "PARAMS" {
			pendingParams += " " + strings.TrimSpace(logText)
		}
		return
	}

	if currentType == "PARAMS" && currentPID != "" {
		broadcastPendingParams()
	}

	if matches := reExecute.FindStringSubmatch(logText); matches != nil {
		timestamp := strings.TrimSpace(matches[1])
		pid := matches[2]
		appName := matches[3]
		sqlStr := matches[4]

		displayID := pid
		if appName != "" {
			displayID = fmt.Sprintf("%s [%s]", appName, pid)
		}

		if watchID != "" && pid != watchID && appName != watchID {
			currentPID = ""
			return
		}

		currentPID = pid
		currentType = "EXECUTE"
		queryState[pid] = pendingQuery{
			SQL:       sqlStr,
			Timestamp: timestamp,
			DisplayID: displayID,
		}
		return
	}

	if matches := reParams.FindStringSubmatch(logText); matches != nil {
		pid := matches[2]
		paramsRaw := matches[4]

		currentPID = pid
		currentType = "PARAMS"
		pendingParams = paramsRaw
		return
	}

	currentPID = ""
	currentType = ""
}

func broadcastPendingParams() {
	pending, ok := queryState[currentPID]
	if ok {
		delete(queryState, currentPID)
		finalQuery := reconstructQuery(pending.SQL, pendingParams)
		msg := QueryMessage{
			PID:       pending.DisplayID,
			Query:     finalQuery,
			Timestamp: pending.Timestamp,
			Table:     extractTable(finalQuery),
		}
		go func(m QueryMessage) {
			broadcast <- m
		}(msg)
	}
	pendingParams = ""
	currentPID = ""
	currentType = ""
}

func extractTable(query string) string {
	patterns := []string{
		`FROM\s+([a-zA-Z0-9_."]+)`,
		`INSERT INTO\s+([a-zA-Z0-9_."]+)`,
		`UPDATE\s+([a-zA-Z0-9_."]+)`,
		`DELETE FROM\s+([a-zA-Z0-9_."]+)`,
		`INTO\s+([a-zA-Z0-9_."]+)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(`(?i)` + p)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	upperQuery := strings.ToUpper(query)
	parts := strings.Fields(upperQuery)
	if len(parts) > 1 {
		if parts[0] == "SELECT" {
			for i, part := range parts {
				if part == "FROM" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		} else if parts[0] == "INSERT" || parts[0] == "UPDATE" || parts[0] == "DELETE" {
			for i, part := range parts {
				if (part == "INTO" || part == "UPDATE" || part == "FROM") && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
	}
	return "N/A"
}

func reconstructQuery(skeleton string, paramsRaw string) string {
	values := make(map[string]string)
	reParamStart := regexp.MustCompile(`\$\d+\s*(?:=|\s|:)\s*`)
	indices := reParamStart.FindAllStringIndex(paramsRaw, -1)

	if len(indices) == 0 {
		return skeleton
	}

	for i := 0; i < len(indices); i++ {
		start := indices[i][0]
		valStart := indices[i][1]
		end := len(paramsRaw)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}

		placeholderMatch := paramsRaw[start:valStart]
		placeholder := strings.TrimSpace(placeholderMatch)
		placeholder = strings.TrimSuffix(placeholder, "=")
		placeholder = strings.TrimSuffix(placeholder, ":")
		placeholder = strings.TrimSpace(placeholder)

		val := paramsRaw[valStart:end]
		val = strings.TrimSuffix(strings.TrimSpace(val), ",")
		val = strings.TrimSuffix(strings.TrimSpace(val), ", ")
		val = strings.TrimSpace(val)

		values[placeholder] = val
	}

	rePlaceholder := regexp.MustCompile(`\$\d+`)
	return rePlaceholder.ReplaceAllStringFunc(skeleton, func(match string) string {
		if val, ok := values[match]; ok {
			return val
		}
		return match
	})
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	clientsMu.Lock()
	clients[ws] = true
	clientsMu.Unlock()
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			clientsMu.Lock()
			delete(clients, ws)
			clientsMu.Unlock()
			break
		}
	}
}

func handleMessages() {
	for {
		msg := <-broadcast
		go saveQuery(msg)
		clientsMu.Lock()
		for client := range clients {
			if err := client.WriteJSON(msg); err != nil {
				client.Close()
				delete(clients, client)
			}
		}
		clientsMu.Unlock()
	}
}
