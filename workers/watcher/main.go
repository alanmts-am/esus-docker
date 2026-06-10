package main

import (
	"bufio"
	"context"
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

	"github.com/redis/go-redis/v9"
)

type QueryMessage struct {
	PID       string `json:"pid"`
	Query     string `json:"query"`
	Timestamp string `json:"timestamp"`
	TableName string `json:"table_name"`
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

	watchID = os.Getenv("WATCH_ID")

	currentPID    string
	currentType   string
	pendingParams string

	rdb *redis.Client
	ctx = context.Background()
)

const redisKey = "watcher:queries"

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func initRedis() {
	addr := getEnv("REDIS_ADDR", "redis:6379")
	rdb = redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Could not connect to Redis at %s: %v", addr, err)
	}
	log.Printf("Connected to Redis at %s", addr)
}

func publish(msg QueryMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal query: %v", err)
		return
	}
	if err := rdb.LPush(ctx, redisKey, data).Err(); err != nil {
		log.Printf("Failed to publish to Redis: %v", err)
	}
}

func main() {
	initRedis()

	containerName := getEnv("TARGET_CONTAINER", "esus_db")
	fmt.Printf("Starting SQL Watcher monitoring container: %s\n", containerName)

	streamDockerLogs(containerName)
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
		log.Fatalf("Error connecting to Docker socket: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Docker API error (%d): %s", resp.StatusCode, string(body))
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
		if _, err = io.ReadFull(reader, payload); err != nil {
			log.Printf("Payload read error: %v", err)
			break
		}

		for _, l := range strings.Split(string(payload), "\n") {
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
		flushPending()
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

func flushPending() {
	pending, ok := queryState[currentPID]
	if ok {
		delete(queryState, currentPID)
		finalQuery := reconstructQuery(pending.SQL, pendingParams)
		msg := QueryMessage{
			PID:       pending.DisplayID,
			Query:     finalQuery,
			Timestamp: pending.Timestamp,
			TableName: extractTable(finalQuery),
		}
		go publish(msg)
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
