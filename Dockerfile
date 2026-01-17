FROM golang:1.24 AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o api ./cmd/api && \
    CGO_ENABLED=1 GOOS=linux go build -o migrate ./cmd/migrate

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    ca-certificates \
    libc6 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/api .
COPY --from=builder /app/migrate .
COPY --from=builder /app/db ./db
COPY --from=builder /app/openapi.yaml .

ARG PORT=8080
EXPOSE $PORT

CMD ["./api"]
