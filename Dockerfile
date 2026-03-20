# --- Этап 1: Сборка ---
FROM golang:1.21-alpine AS builder

# Установка зависимостей для сборки (если нужно CGO, но для Go в докере лучше CGO_ENABLED=0)
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Копируем файлы модулей для кэширования зависимостей
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходный код
COPY . .

# Сборка бинарника (статическая линковка)
# -ldflags "-s -w" убирает отладочную информацию, уменьшая размер
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-s -w" -o main ./cmd/main.go

# --- Этап 2: Рантайм ---
FROM alpine:latest

# Установка корневых сертификатов (нужно для HTTPS запросов)
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Копируем бинарник из этапа сборки
COPY --from=builder /app/main .

# Создаем не-root пользователя для безопасности
RUN adduser -D -g '' appuser
USER appuser

# Порт, который слушает приложение
EXPOSE 8080

# Команда запуска
CMD ["./main"]