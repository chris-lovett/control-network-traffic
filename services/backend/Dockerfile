# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o backend .

# Runtime stage – distroless for minimal attack surface
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/backend /backend
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/backend"]
