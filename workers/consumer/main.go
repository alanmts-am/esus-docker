package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

type QueryMessage struct {
	PID       string `json:"pid"`
	Query     string `json:"query"`
	Timestamp string `json:"timestamp"`
	TableName string `json:"table_name"`
}

const redisKey = "watcher:queries"

var (
	rdb *redis.Client
	db  *sql.DB
	ctx = context.Background()
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func initRedis() {
	addr := getEnv("REDIS_ADDR", "redis:6379")
	rdb = redis.NewClient(&redis.Options{Addr: addr})

	for i := range 10 {
		if err := rdb.Ping(ctx).Err(); err == nil {
			break
		} else if i == 9 {
			log.Fatalf("Could not connect to Redis at %s: %v", addr, err)
		} else {
			log.Printf("Waiting for Redis (attempt %d/10)", i+1)
			time.Sleep(3 * time.Second)
		}
	}
	log.Printf("Connected to Redis at %s", addr)
}

func initDB() {
	host := getEnv("PG_HOST", "db")
	port := getEnv("PG_PORT", "5432")
	user := getEnv("PG_USER", "postgres")
	password := getEnv("PG_PASSWORD", "esus")
	dbName := getEnv("PG_DBNAME", "watcher")

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
		msg.PID, msg.Query, msg.Timestamp, msg.TableName,
	)
	if err != nil {
		log.Printf("Failed to save query: %v", err)
	}
}

func main() {
	initRedis()
	initDB()

	log.Println("Consumer started, waiting for messages...")

	for {
		result, err := rdb.BRPop(ctx, 0, redisKey).Result()
		if err != nil {
			log.Printf("BRPop error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		// BRPop returns [key, value]
		if len(result) < 2 {
			continue
		}

		var msg QueryMessage
		if err := json.Unmarshal([]byte(result[1]), &msg); err != nil {
			log.Printf("Failed to unmarshal message: %v", err)
			continue
		}

		saveQuery(msg)
		log.Printf("Saved query from PID %s on table %s", msg.PID, msg.TableName)
	}
}
