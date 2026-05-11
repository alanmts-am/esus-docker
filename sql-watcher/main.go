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

var (
	// Regex patterns: Case-insensitive and flexible with spaces
	reExecute = regexp.MustCompile(`(?i)\[(.*?)\] LOG:\s+execute .*: (.*)`)
	reParams  = regexp.MustCompile(`(?i)\[(.*?)\] DETAIL:\s+parameters: (.*)`)
	
	queryState = make(map[string]string)
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

type QueryMessage struct {
	PID       string `json:"pid"`
	Query     string `json:"query"`
	Timestamp string `json:"timestamp"`
}

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
	// Create a custom HTTP client that talks to the Docker Unix Socket
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

	// Docker log stream has an 8-byte header per frame: [1-byte stream type, 3-bytes pad, 4-bytes payload size]
	// If the container has a TTY, the header is not present. But most DB containers don't use TTY.
	reader := bufio.NewReader(resp.Body)
	for {
		header := make([]byte, 8)
		_, err := io.ReadFull(reader, header)
		if err != nil {
			if err == io.EOF { break }
			log.Printf("Header read error: %v", err)
			break
		}

		// Payload size is in the last 4 bytes (BigEndian)
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
		pid := matches[1]

		// Filter by ID if configured
		if watchID != "" && pid != watchID {
			return
		}

		sql := matches[2]
		stateMu.Lock()
		queryState[pid] = sql
		stateMu.Unlock()
		return
	}

	if matches := reParams.FindStringSubmatch(logText); matches != nil {
		pid := matches[1]
		paramsRaw := matches[2]
		stateMu.Lock()
		sqlSkeleton, ok := queryState[pid]
		delete(queryState, pid)
		stateMu.Unlock()

		if ok {
			finalQuery := reconstructQuery(sqlSkeleton, paramsRaw)
			broadcast <- QueryMessage{
				PID:   pid,
				Query: finalQuery,
			}
		}
	}
}

func reconstructQuery(skeleton string, paramsRaw string) string {
	// 1. Encontrar todas as posições onde começa um "$n = "
	reParamStart := regexp.MustCompile(`\$\d+ = `)
	indices := reParamStart.FindAllStringIndex(paramsRaw, -1)
	if len(indices) == 0 {
		return skeleton
	}

	result := skeleton
	for i := 0; i < len(indices); i++ {
		// Onde começa o "$n = " atual
		start := indices[i][0]
		valStart := indices[i][1]

		// Onde termina o valor (é o início do próximo "$n = " ou o fim da string)
		end := len(paramsRaw)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}

		// Extrair placeholder ($n) e o valor
		placeholder := strings.TrimSpace(strings.Split(paramsRaw[start:valStart], " =")[0])
		val := paramsRaw[valStart:end]

		// Limpar o valor (remover vírgula e espaço no final se não for o último)
		val = strings.TrimSuffix(val, ", ")
		val = strings.TrimSpace(val)

		// Substituir no esqueleto
		result = strings.ReplaceAll(result, placeholder, val)
	}

	return result
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
