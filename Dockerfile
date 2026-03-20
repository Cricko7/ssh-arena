FROM golang:1.23-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /out/grpc-game ./cmd/grpc-game
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /out/ssh-server ./cmd/ssh-server

FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata openssh

COPY --from=build /out/grpc-game /app/bin/grpc-game
COPY --from=build /out/ssh-server /app/bin/ssh-server
COPY config /app/config
COPY events /app/events
COPY config.yaml /app/config.yaml

RUN adduser -D -g '' appuser \
    && mkdir -p /var/lib/ssh-arena/ssh /app/data \
    && chown -R appuser:appuser /app /var/lib/ssh-arena

USER appuser

EXPOSE 9090 2222

CMD ["/app/bin/grpc-game"]
