# ─── Stage 1: Build frontend SPA ───────────────────────────────────────────
FROM node:22-alpine AS frontend
WORKDIR /build
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ─── Stage 2: Build Go binary (CGO required for sqlite3 + fts5) ────────────
FROM golang:1.25-bookworm AS builder
WORKDIR /build

# CGO needs gcc; bookworm has it, just need to install
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Download deps first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source (desktop/ excluded via .dockerignore)
COPY . .

# Embed frontend output from stage 1
COPY --from=frontend /build/dist internal/web/dist/

# Build binary: CGO on, fts5 tag, stripped for size
RUN CGO_ENABLED=1 go build \
    -tags fts5 \
    -ldflags="-s -w" \
    -trimpath \
    -o agentsview \
    ./cmd/agentsview

# ─── Stage 3: Minimal runtime ──────────────────────────────────────────────
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js (for codex + gemini CLIs)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install Claude CLI
RUN curl -fsSL https://claude.ai/install.sh | bash \
    && ln -s /root/.local/bin/claude /usr/local/bin/claude

# Install Codex and Gemini CLIs
RUN npm install -g @openai/codex @google/gemini-cli

COPY --from=builder /build/agentsview /usr/local/bin/agentsview

EXPOSE 8080

ENTRYPOINT ["agentsview"]
