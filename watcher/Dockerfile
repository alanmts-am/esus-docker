# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install git for private dependencies if needed
RUN apk add --no-cache git

# Copy source code
COPY . .

# Download dependencies and generate go.sum
RUN go mod tidy

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o sql-watcher .

# Run stage
FROM alpine:latest

WORKDIR /root/

# Copy the binary and static files
COPY --from=builder /app/sql-watcher .
COPY --from=builder /app/static ./static

# Expose port
EXPOSE 8081

# Set default log path
ENV PG_LOG_PATH=/logs/postgresql.log

# Command to run
CMD ["./sql-watcher"]
