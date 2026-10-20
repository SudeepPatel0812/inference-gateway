# 1. build the chat UI (web/) into static files
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. build the Go gateway
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /server ./cmd/server

# 3. final image: the binary plus the UI it serves at /
FROM gcr.io/distroless/static-debian12
COPY --from=build /server /server
COPY --from=web /web/dist /web
ENV STATIC_DIR=/web
EXPOSE 8080
ENTRYPOINT ["/server"]
