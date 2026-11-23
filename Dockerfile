# Stage 1: Build pure-Go KlipForge binary with embedded dashboard
FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/klipforge .

# Stage 2: Production Linux VPS runtime with FFmpeg + yt-dlp + Node.js JS runtime + fonts
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl ffmpeg python3 nodejs fonts-dejavu-core fonts-liberation \
    && curl -sL https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp \
    && chmod +x /usr/local/bin/yt-dlp \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/klipforge /app/klipforge
RUN mkdir -p /app/data /app/outputs

EXPOSE 8787
ENTRYPOINT ["/app/klipforge", "--host", "0.0.0.0", "--port", "8787", "--no-open"]
