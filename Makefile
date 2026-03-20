.PHONY: build run test docker-build docker-run

build:
	go build -o bin/main ./cmd/main.go

run:
	go run ./cmd/main.go

test:
	go test ./...

docker-build:
	docker build -t my-go-app:latest .

docker-run:
	docker-compose up --build