# --- dashboard ---
FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- binary (CGO-free: the SQLite driver is pure Go) ---
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /microserver ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata su-exec && adduser -D -u 10001 app && mkdir /data && chown app /data
COPY --from=build /microserver /usr/local/bin/microserver
# The entrypoint fixes /data ownership as root, then runs the app as "app".
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN sed -i 's/\r$//' /usr/local/bin/docker-entrypoint.sh && chmod 755 /usr/local/bin/docker-entrypoint.sh
ENV DATA_DIR=/data APP_PORT=8090
VOLUME /data
EXPOSE 8090
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["microserver"]
