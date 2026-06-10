FROM golang:1.23-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY . .

RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -o sql-consumer .

FROM alpine:latest

WORKDIR /root/

COPY --from=builder /app/sql-consumer .

CMD ["./sql-consumer"]
