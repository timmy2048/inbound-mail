.PHONY: test run up

test:
	go test ./...

run:
	go run ./cmd/laijian

up:
	docker compose up --build
