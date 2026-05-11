package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

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
}

var (
	// Regex patterns: Case-insensitive and flexible with spaces
	// Updated to optionally capture timestamp before [PID]
	reExecute = regexp.MustCompile(`(?i)(?:(.*?) )?\[(.*?)\] LOG:\s+execute .*: (.*)`)
	reParams  = regexp.MustCompile(`(?i)(?:(.*?) )?\[(.*?)\] DETAIL:\s+parameters: (.*)`)
	
	queryState = make(map[string]pendingQuery)
	stateMu    sync.Mutex

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	clients   = make(map[*websocket.Conn]bool)
	clientsMu sync.Mutex
	broadcast = make(chan QueryMessage)

	// Filter settings
	watchID = os.Getenv("WATCH_ID")
)

func main() {
	containerName := os.Getenv("TARGET_CONTAINER")
	if containerName == "" {
		containerName = "esus_db_5_4_36_postgre18"
	}

	fmt.Printf("Starting SQL Watcher monitoring container: %s via /var/run/docker.sock\n", containerName)

	go streamDockerLogs(containerName)
	go handleMessages()

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
			if err == io.EOF { break }
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
	if matches := reExecute.FindStringSubmatch(logText); matches != nil {
		timestamp := strings.TrimSpace(matches[1])
		pid := matches[2]
		sql := matches[3]

		if watchID != "" && pid != watchID {
			return
		}

		stateMu.Lock()
		queryState[pid] = pendingQuery{
			SQL:       sql,
			Timestamp: timestamp,
		}
		stateMu.Unlock()
		return
	}

	if matches := reParams.FindStringSubmatch(logText); matches != nil {
		pid := matches[2]
		paramsRaw := matches[3]
		
		stateMu.Lock()
		pending, ok := queryState[pid]
		delete(queryState, pid)
		stateMu.Unlock()

		if ok {
			finalQuery := reconstructQuery(pending.SQL, paramsRaw)
			broadcast <- QueryMessage{
				PID:       pid,
				Query:     finalQuery,
				Timestamp: pending.Timestamp,
				Table:     extractTable(finalQuery),
			}
		}
	}
}

func extractTable(query string) string {
	upperQuery := strings.ToUpper(query)
	patterns := []string{
		`FROM\s+([a-zA-Z0-9_."]+)`,
		`INSERT INTO\s+([a-zA-Z0-9_."]+)`,
		`UPDATE\s+([a-zA-Z0-9_."]+)`,
		`DELETE FROM\s+([a-zA-Z0-9_."]+)`,
		`INTO\s+([a-zA-Z0-9_."]+)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(`(?i)` + p)
		matches := re.FindStringSubmatch(query)
		if len(matches) > 1 {
			return matches[1]
		}
	}
	
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
	// 1. Build a map of placeholder -> value
	values := make(map[string]string)
	reParamStart := regexp.MustCompile(`\$\d+ = `)
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
		
		// Extract placeholder (e.g., "$1")
		placeholder := strings.TrimSpace(strings.Split(paramsRaw[start:valStart], " =")[0])
		
		// Extract and clean value
		val := paramsRaw[valStart:end]
		val = strings.TrimSuffix(val, ", ")
		val = strings.TrimSpace(val)
		
		values[placeholder] = val
	}

	// 2. Replace placeholders in the skeleton using regex to avoid partial matches
	// Using ReplaceAllStringFunc ensures that $10 is treated as a single token 
	// and not as $1 followed by 0.
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
	if err != nil { return }
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
