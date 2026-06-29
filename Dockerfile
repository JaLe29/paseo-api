### BUILD
FROM golang:1.23-alpine AS build

WORKDIR /src

# Manifests first — the module cache stays warm until dependencies change.
COPY go.mod go.sum ./
RUN go mod download

# Source + static build (no CGO → runs on scratch/distroless).
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/paseo-api ./cmd/server

### PRODUCTION
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/paseo-api /app/paseo-api

# PASEO_HOST is required (IP:port of the paseo instance) — pass it at runtime via -e.
ENV PORT=3000
EXPOSE 3000

USER nonroot:nonroot
ENTRYPOINT ["/app/paseo-api"]
