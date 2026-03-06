FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o /worker .

FROM alpine:3.19
RUN apk add --no-cache ffmpeg
COPY --from=builder /worker /usr/local/bin/worker
CMD ["/usr/local/bin/worker"]
