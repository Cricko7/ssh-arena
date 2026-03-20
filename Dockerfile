FROM golang:1.23-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/grpc-game ./cmd/grpc-game
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/ssh-server ./cmd/ssh-server

FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates openssh

COPY --from=build /out/grpc-game /app/bin/grpc-game
COPY --from=build /out/ssh-server /app/bin/ssh-server
COPY config /app/config
COPY events /app/events
COPY config.yaml /app/config.yaml
COPY docker /app/docker

RUN chmod +x /app/docker/*.sh

EXPOSE 9090 2222

CMD ["/app/bin/grpc-game"]
