FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o gateway .

FROM alpine:3.19
RUN apk add --no-cache ffmpeg curl
COPY --from=builder /app/gateway /usr/local/bin/gateway
EXPOSE 8000
CMD ["gateway"]
