# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o frontend .

# Runtime stage – distroless for minimal attack surface
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/frontend /frontend
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/frontend"]
